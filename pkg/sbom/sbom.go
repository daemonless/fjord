// Package sbom reads what an image says about its own contents, from the
// registry, so an update can say what it changes before anything is pulled.
//
// Two places an image can carry an SBOM, tried in order:
//   - a BuildKit attestation inside the tag's index (Docker Hub's official
//     images, anything built with `docker buildx --sbom`): SPDX.
//   - a cosign attestation at the tag "sha256-<digest>.att" (sigstore users,
//     daemonless): CycloneDX, or SPDX.
//
// Most images carry neither, and that is not an error: the image config's own
// labels (version, build date) are read for every image, so a stack with no
// SBOMs still gets a version line.
package sbom

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/daemonless/fjord/pkg/registry"
)

// Doc is what one image says about itself.
type Doc struct {
	Source   string            `json:"source,omitempty"` // "spdx" | "cyclonedx" | "" (labels only)
	Version  string            `json:"version,omitempty"`
	Created  string            `json:"created,omitempty"`
	Packages map[string]string `json:"-"` // name -> version; nil without an SBOM
}

// Change is one package whose version moved.
type Change struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Package is one package added or removed.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Diff is what an update changes, as far as the two images say.
type Diff struct {
	VersionFrom string `json:"versionFrom,omitempty"`
	VersionTo   string `json:"versionTo,omitempty"`
	CreatedFrom string `json:"createdFrom,omitempty"`
	CreatedTo   string `json:"createdTo,omitempty"`
	// Packages is true only when BOTH images carry an SBOM; the lists below
	// mean nothing otherwise, and an empty diff then says "nothing changed".
	Packages bool      `json:"packages"`
	Source   string    `json:"source,omitempty"`
	Changed  []Change  `json:"changed,omitempty"`
	Added    []Package `json:"added,omitempty"`
	Removed  []Package `json:"removed,omitempty"`
}

// Compare is new against old. Either may be nil (unknown).
func Compare(old, new *Doc) Diff {
	var d Diff
	if old != nil {
		d.VersionFrom, d.CreatedFrom = old.Version, old.Created
	}
	if new != nil {
		d.VersionTo, d.CreatedTo = new.Version, new.Created
	}
	if old == nil || new == nil || old.Packages == nil || new.Packages == nil {
		return d
	}
	d.Packages, d.Source = true, new.Source
	for name, to := range new.Packages {
		from, ok := old.Packages[name]
		switch {
		case !ok:
			d.Added = append(d.Added, Package{name, to})
		case from != to:
			d.Changed = append(d.Changed, Change{name, from, to})
		}
	}
	for name, from := range old.Packages {
		if _, ok := new.Packages[name]; !ok {
			d.Removed = append(d.Removed, Package{name, from})
		}
	}
	sort.Slice(d.Changed, func(i, j int) bool { return d.Changed[i].Name < d.Changed[j].Name })
	sort.Slice(d.Added, func(i, j int) bool { return d.Added[i].Name < d.Added[j].Name })
	sort.Slice(d.Removed, func(i, j int) bool { return d.Removed[i].Name < d.Removed[j].Name })
	return d
}

// Registry calls, swapped out by tests.
var (
	manifest = registry.Manifest
	blob     = registry.Blob
)

const (
	acceptIndex    = "application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json"
	acceptManifest = "application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json"
	// An SBOM for a large app runs to megabytes (Docker Hub's redis SPDX is
	// 2 MB); a cap keeps a hostile registry from filling memory.
	sbomLimit = 64 << 20
)

// cache holds Docs by platform digest. A digest names immutable content, so
// an entry never goes stale; it is only bounded.
var cache = struct {
	sync.Mutex
	m map[string]*Doc
}{m: map[string]*Doc{}}

// For is what the image at platform (a manifest digest) says about itself.
// index is the tag's index digest when the image is multi-arch, "" or equal
// to platform when it is not.
func For(ctx context.Context, image, index, platform string) (*Doc, error) {
	cache.Lock()
	if d, ok := cache.m[platform]; ok {
		cache.Unlock()
		return d, nil
	}
	cache.Unlock()

	d := &Doc{}
	if err := readLabels(ctx, image, platform, d); err != nil {
		return nil, err
	}
	pkgs, src := buildkitSBOM(ctx, image, index, platform)
	if pkgs == nil {
		pkgs, src = cosignSBOM(ctx, image, platform)
	}
	d.Packages, d.Source = pkgs, src

	cache.Lock()
	if len(cache.m) > 256 {
		cache.m = map[string]*Doc{}
	}
	cache.m[platform] = d
	cache.Unlock()
	return d, nil
}

// readLabels takes version and build date from the image config.
func readLabels(ctx context.Context, image, platform string, d *Doc) error {
	body, _, err := manifest(ctx, image, platform, acceptManifest)
	if err != nil {
		return err
	}
	var m struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &m); err != nil || m.Config.Digest == "" {
		return errors.New("not an image manifest")
	}
	cfg, err := blob(ctx, image, m.Config.Digest, 4<<20)
	if err != nil {
		return err
	}
	var c struct {
		Created string `json:"created"`
		Config  struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(cfg, &c); err != nil {
		return err
	}
	d.Version = c.Config.Labels["org.opencontainers.image.version"]
	d.Created = c.Created
	if d.Created == "" {
		d.Created = c.Config.Labels["org.opencontainers.image.created"]
	}
	return nil
}

// buildkitSBOM finds the attestation manifest the index keeps for platform
// and reads its SPDX layer (an in-toto statement, unsigned).
func buildkitSBOM(ctx context.Context, image, index, platform string) (map[string]string, string) {
	if index == "" || index == platform {
		return nil, ""
	}
	body, _, err := manifest(ctx, image, index, acceptIndex)
	if err != nil {
		return nil, ""
	}
	var idx struct {
		Manifests []struct {
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if json.Unmarshal(body, &idx) != nil {
		return nil, ""
	}
	for _, m := range idx.Manifests {
		if m.Annotations["vnd.docker.reference.type"] != "attestation-manifest" ||
			m.Annotations["vnd.docker.reference.digest"] != platform {
			continue
		}
		for _, l := range attestationLayers(ctx, image, m.Digest, "in-toto.io/predicate-type") {
			if l.kind != "spdx" {
				continue
			}
			raw, err := blob(ctx, image, l.digest, sbomLimit)
			if err != nil {
				continue
			}
			if pkgs := parseStatement(raw, l.kind); pkgs != nil {
				return pkgs, l.kind
			}
		}
	}
	return nil, ""
}

// cosignSBOM reads the attestation cosign stores at "sha256-<hex>.att": each
// layer a DSSE envelope whose payload is an in-toto statement.
func cosignSBOM(ctx context.Context, image, platform string) (map[string]string, string) {
	tag := strings.Replace(platform, ":", "-", 1) + ".att"
	layers := attestationLayers(ctx, image, tag, "predicateType")
	// CycloneDX first: it names the app itself as well as its packages.
	sort.SliceStable(layers, func(i, j int) bool { return layers[i].kind == "cyclonedx" && layers[j].kind != "cyclonedx" })
	for _, l := range layers {
		raw, err := blob(ctx, image, l.digest, sbomLimit)
		if err != nil {
			continue
		}
		var env struct {
			Payload string `json:"payload"`
		}
		if json.Unmarshal(raw, &env) != nil || env.Payload == "" {
			continue
		}
		stmt, err := base64.StdEncoding.DecodeString(env.Payload)
		if err != nil {
			continue
		}
		if pkgs := parseStatement(stmt, l.kind); pkgs != nil {
			return pkgs, l.kind
		}
	}
	return nil, ""
}

type sbomLayer struct{ digest, kind string }

// attestationLayers lists a manifest's SBOM layers, typed by the predicate
// annotation (BuildKit and cosign name it differently).
func attestationLayers(ctx context.Context, image, ref, annotation string) []sbomLayer {
	body, _, err := manifest(ctx, image, ref, acceptManifest)
	if err != nil {
		return nil
	}
	var m struct {
		Layers []struct {
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"layers"`
	}
	if json.Unmarshal(body, &m) != nil {
		return nil
	}
	var out []sbomLayer
	for _, l := range m.Layers {
		if k := predicateKind(l.Annotations[annotation]); k != "" {
			out = append(out, sbomLayer{l.Digest, k})
		}
	}
	return out
}

func predicateKind(predicateType string) string {
	switch {
	case strings.Contains(predicateType, "cyclonedx"):
		return "cyclonedx"
	case strings.Contains(predicateType, "spdx"):
		return "spdx"
	}
	return ""
}

// parseStatement reads an in-toto statement's predicate as an SBOM of kind.
func parseStatement(raw []byte, kind string) map[string]string {
	var st struct {
		Predicate json.RawMessage `json:"predicate"`
	}
	if json.Unmarshal(raw, &st) != nil || len(st.Predicate) == 0 {
		return nil
	}
	if kind == "cyclonedx" {
		return parseCycloneDX(st.Predicate)
	}
	return parseSPDX(st.Predicate)
}

func parseCycloneDX(raw []byte) map[string]string {
	var bom struct {
		Components []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"components"`
	}
	if json.Unmarshal(raw, &bom) != nil {
		return nil
	}
	out := map[string]string{}
	for _, c := range bom.Components {
		add(out, c.Name, c.Version)
	}
	return out
}

func parseSPDX(raw []byte) map[string]string {
	var doc struct {
		Packages []struct {
			Name        string `json:"name"`
			VersionInfo string `json:"versionInfo"`
		} `json:"packages"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	out := map[string]string{}
	for _, p := range doc.Packages {
		add(out, p.Name, p.VersionInfo)
	}
	return out
}

// add records a package. A name listed twice at different versions (a
// library vendored in two places) keeps both, so a change in either shows.
func add(m map[string]string, name, version string) {
	if name == "" {
		return
	}
	if cur, ok := m[name]; ok && cur != version {
		vs := strings.Split(cur, ", ")
		for _, v := range vs {
			if v == version {
				return
			}
		}
		vs = append(vs, version)
		sort.Strings(vs)
		m[name] = strings.Join(vs, ", ")
		return
	}
	m[name] = version
}
