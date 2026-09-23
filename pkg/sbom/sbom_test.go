package sbom

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/daemonless/fjord/pkg/registry"
)

// fakeRegistry serves manifests by ref and blobs by digest; anything else is
// registry.ErrNotFound, as a registry without the attestation answers.
func fakeRegistry(t *testing.T, manifests map[string]any, blobs map[string]any) {
	t.Helper()
	oldM, oldB := manifest, blob
	t.Cleanup(func() { manifest, blob = oldM, oldB; cache.m = map[string]*Doc{} })
	cache.m = map[string]*Doc{}
	enc := func(v any) []byte {
		if b, ok := v.([]byte); ok {
			return b
		}
		b, _ := json.Marshal(v)
		return b
	}
	manifest = func(_ context.Context, _, ref, _ string) ([]byte, string, error) {
		if v, ok := manifests[ref]; ok {
			return enc(v), "", nil
		}
		return nil, "", registry.ErrNotFound
	}
	blob = func(_ context.Context, _, digest string, _ int64) ([]byte, error) {
		if v, ok := blobs[digest]; ok {
			return enc(v), nil
		}
		return nil, registry.ErrNotFound
	}
}

// image is a platform manifest whose config carries a version label.
func image(version, created string) (manifest map[string]any, config map[string]any) {
	return map[string]any{"config": map[string]any{"digest": "sha256:cfg-" + version}},
		map[string]any{"created": created, "config": map[string]any{"Labels": map[string]string{"org.opencontainers.image.version": version}}}
}

func statement(predicate any) map[string]any { return map[string]any{"predicate": predicate} }

func TestCosignCycloneDX(t *testing.T) {
	m, cfg := image("10.11.19", "2026-09-22T10:44:02Z")
	cdx := statement(map[string]any{"components": []map[string]string{{"name": "openssl", "version": "3.0.16"}, {"name": "curl", "version": "8.22.0"}}})
	payload, _ := json.Marshal(cdx)
	fakeRegistry(t,
		map[string]any{
			"sha256:amd64": m,
			"sha256-amd64.att": map[string]any{"layers": []map[string]any{
				{"digest": "sha256:spdx-layer", "annotations": map[string]string{"predicateType": "https://spdx.dev/Document"}},
				{"digest": "sha256:cdx-layer", "annotations": map[string]string{"predicateType": "https://cyclonedx.org/bom"}},
			}},
		},
		map[string]any{
			"sha256:cfg-10.11.19": cfg,
			"sha256:cdx-layer":    map[string]string{"payload": base64.StdEncoding.EncodeToString(payload)},
		})
	d, err := For(context.Background(), "ghcr.io/x/mariadb:10.11", "sha256:index", "sha256:amd64")
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "cyclonedx" || d.Version != "10.11.19" || len(d.Packages) != 2 || d.Packages["openssl"] != "3.0.16" {
		t.Errorf("doc = %+v", d)
	}
}

// Docker Hub's shape: the attestation lives in the index, pointing back at
// the platform manifest it describes, as an unsigned in-toto statement.
func TestBuildKitSPDX(t *testing.T) {
	m, cfg := image("", "2026-09-21T17:33:48Z")
	fakeRegistry(t,
		map[string]any{
			"sha256:index": map[string]any{"manifests": []map[string]any{
				{"digest": "sha256:amd64"},
				{"digest": "sha256:att-arm64", "annotations": map[string]string{"vnd.docker.reference.type": "attestation-manifest", "vnd.docker.reference.digest": "sha256:arm64"}},
				{"digest": "sha256:att-amd64", "annotations": map[string]string{"vnd.docker.reference.type": "attestation-manifest", "vnd.docker.reference.digest": "sha256:amd64"}},
			}},
			"sha256:amd64": m,
			"sha256:att-amd64": map[string]any{"layers": []map[string]any{
				{"digest": "sha256:prov", "annotations": map[string]string{"in-toto.io/predicate-type": "https://slsa.dev/provenance/v0.2"}},
				{"digest": "sha256:spdx", "annotations": map[string]string{"in-toto.io/predicate-type": "https://spdx.dev/Document"}},
			}},
		},
		map[string]any{
			"sha256:cfg-": cfg,
			"sha256:spdx": statement(map[string]any{"packages": []map[string]string{{"name": "redis", "versionInfo": "8.2.1"}}}),
		})
	d, err := For(context.Background(), "redis:latest", "sha256:index", "sha256:amd64")
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "spdx" || d.Packages["redis"] != "8.2.1" {
		t.Errorf("doc = %+v", d)
	}
}

// No SBOM anywhere -- most images. Labels still give a version line, and
// Packages stays nil so nobody reads "no changes" into it.
func TestLabelsOnly(t *testing.T) {
	m, cfg := image("1.2.3", "2026-09-20T00:00:00Z")
	fakeRegistry(t, map[string]any{"sha256:amd64": m}, map[string]any{"sha256:cfg-1.2.3": cfg})
	d, err := For(context.Background(), "example.org/app:latest", "sha256:index", "sha256:amd64")
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "" || d.Packages != nil || d.Version != "1.2.3" {
		t.Errorf("doc = %+v", d)
	}
}

func TestCompare(t *testing.T) {
	old := &Doc{Source: "cyclonedx", Version: "1.0", Packages: map[string]string{"openssl": "3.0.15", "curl": "8.22.0", "gone": "1"}}
	new := &Doc{Source: "cyclonedx", Version: "1.1", Packages: map[string]string{"openssl": "3.0.16", "curl": "8.22.0", "fresh": "2"}}
	d := Compare(old, new)
	if !d.Packages || d.VersionFrom != "1.0" || d.VersionTo != "1.1" {
		t.Errorf("header = %+v", d)
	}
	if !reflect.DeepEqual(d.Changed, []Change{{"openssl", "3.0.15", "3.0.16"}}) {
		t.Errorf("changed = %v", d.Changed)
	}
	if !reflect.DeepEqual(d.Added, []Package{{"fresh", "2"}}) || !reflect.DeepEqual(d.Removed, []Package{{"gone", "1"}}) {
		t.Errorf("added %v removed %v", d.Added, d.Removed)
	}

	// Only one side has an SBOM: no package verdict at all. An empty list
	// here would read as "rebuild, nothing changed", which nobody knows.
	d = Compare(&Doc{Version: "1.0"}, new)
	if d.Packages || d.Changed != nil || d.VersionFrom != "1.0" || d.VersionTo != "1.1" {
		t.Errorf("one-sided = %+v", d)
	}
	if d := Compare(nil, nil); d.Packages {
		t.Errorf("nil docs = %+v", d)
	}
}

// A library vendored twice at different versions keeps both.
func TestDuplicatePackageNames(t *testing.T) {
	m := map[string]string{}
	add(m, "lodash", "4.17.21")
	add(m, "lodash", "4.17.20")
	add(m, "lodash", "4.17.21")
	if m["lodash"] != "4.17.20, 4.17.21" {
		t.Errorf("lodash = %q", m["lodash"])
	}
}
