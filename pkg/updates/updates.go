// Package updates detects whether a stack's images are behind what its
// registry tags currently offer. It is runtime-agnostic: local image state
// comes through engine.Backend, registry state through pkg/registry.
package updates

import (
	"context"
	"slices"
	"strings"

	"github.com/daemonless/fjord/pkg/registry"
)

// Status is the result of an update-availability check for a stack. Two kinds
// of "behind" are distinguished because they need different actions:
//   - "available": the current tag's bytes moved upstream -> Update (pull) applies it.
//   - "upgrade":   a newer VERSION exists in the tag's train -> Change Version
//     applies it (Update wouldn't, since the compose still names the old tag).
//
// The top-level fields summarise the stack for a badge; Services says which
// part of it is behind. A stack of four images used to report only the first
// drifted one, with no service or image named, so "release moved" could be
// any of them.
type Status struct {
	State       string          `json:"state"`                 // current | available | upgrade | pinned | unknown
	Tag         string          `json:"tag,omitempty"`         // the ref's tag, for display
	Latest      string          `json:"latest,omitempty"`      // registry (index) digest, when "available"
	NewTag      string          `json:"newTag,omitempty"`      // newer version's tag, when "upgrade"
	FromVersion string          `json:"fromVersion,omitempty"` // e.g. "2.16.0", when "upgrade"
	ToVersion   string          `json:"toVersion,omitempty"`   // e.g. "2.17.0", when "upgrade"
	Detail      string          `json:"detail,omitempty"`      // why the state is "unknown", for the badge tooltip
	Services    []ServiceStatus `json:"services,omitempty"`
}

// ServiceStatus is one service's part of a Status.
type ServiceStatus struct {
	Service     string `json:"service"`
	Image       string `json:"image"`
	State       string `json:"state"`
	Tag         string `json:"tag,omitempty"`
	Running     string `json:"running,omitempty"` // digest the container was created from, when known
	Latest      string `json:"latest,omitempty"`  // registry digest for the tag, when "available"
	NewTag      string `json:"newTag,omitempty"`
	FromVersion string `json:"fromVersion,omitempty"`
	ToVersion   string `json:"toVersion,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// Service is one service to check.
type Service struct {
	Name  string
	Image string // ${VAR}s already expanded
	// Running is the registry digest the service's container was created
	// from; "" when it has no container or the engine cannot say. Given, it
	// is what "current" is judged by. Absent, the local tag is -- which is
	// wrong as soon as anything else pulls that tag: redis is in eight stacks,
	// and updating one made the other seven read "current" on the old bytes.
	Running string
	// Known is every digest the running image answers to (Running among
	// them). Matching any of them means the service already has those bytes.
	Known []string
}

// LocalImages is the one engine call the check needs: what a tag points to in
// the local store, for services with no running digest.
type LocalImages interface {
	ImageRepoDigests(ctx context.Context, ref string) ([]string, error)
}

// SchemeFor resolves an image repo to the tag scheme its catalog entry
// declares, or nil when nothing declares one (an adopted stack on a
// third-party image) -- in which case the scheme is inferred from the tags.
type SchemeFor func(repo string) *registry.Scheme

// Registry lookups, swapped out by tests.
var (
	registryDigest   = registry.Digest
	registryPlatform = registry.PlatformDigest
	registryTrains   = registry.Trains
)

// Check evaluates every service and aggregates: any service with a digest
// drift => "available"; else any with a newer version => "upgrade"; else any
// not confirmable => "unknown"; else all frozen => "pinned"; else "current".
// A service the registry cannot answer for is "unknown" on its own rather
// than failing the whole stack. schemeFor may be nil.
func Check(ctx context.Context, local LocalImages, svcs []Service, schemeFor SchemeFor) Status {
	if len(svcs) == 0 {
		return Status{State: "unknown", Detail: "no images to check"}
	}
	out := Status{Services: make([]ServiceStatus, 0, len(svcs))}
	for _, sv := range svcs {
		out.Services = append(out.Services, evalService(ctx, local, sv, schemeFor))
	}
	summarise(&out)
	return out
}

// summarise fills the top-level fields from the per-service ones. The
// precedence is the one the badge has always had: the service it picks is
// the one whose action matters most.
func summarise(st *Status) {
	pick := func(state string) *ServiceStatus {
		for i := range st.Services {
			if st.Services[i].State == state {
				return &st.Services[i]
			}
		}
		return nil
	}
	allPinned := true
	for _, s := range st.Services {
		if s.State != "pinned" {
			allPinned = false
		}
	}
	var chosen *ServiceStatus
	switch {
	case pick("available") != nil:
		chosen = pick("available")
	case pick("upgrade") != nil:
		chosen = pick("upgrade")
	case pick("unknown") != nil:
		chosen = pick("unknown")
	case allPinned:
		chosen = &st.Services[0]
	default:
		st.State, st.Tag = "current", st.Services[0].Tag
		return
	}
	st.State, st.Tag, st.Latest = chosen.State, chosen.Tag, chosen.Latest
	st.NewTag, st.FromVersion, st.ToVersion, st.Detail = chosen.NewTag, chosen.FromVersion, chosen.ToVersion, chosen.Detail
}

// evalService classifies one service. Pinned refs are frozen. A version-pin
// tag (digit-leading, e.g. "2.16.0-pkg") is checked against its train for a
// newer release first; otherwise (and for rolling tags) it falls back to a
// digest-drift check against the registry.
func evalService(ctx context.Context, local LocalImages, sv Service, schemeFor SchemeFor) ServiceStatus {
	tag := imageTag(sv.Image)
	base := ServiceStatus{Service: sv.Name, Image: sv.Image, Tag: tag, Running: sv.Running}
	with := func(state string) ServiceStatus { b := base; b.State = state; return b }
	if strings.Contains(sv.Image, "@sha256:") {
		return with("pinned")
	}
	if isVersionTag(tag) {
		if up, ok := newerVersion(ctx, registry.Repo(sv.Image), tag, schemeFor); ok {
			b := with("upgrade")
			b.NewTag, b.FromVersion, b.ToVersion = up.NewTag, up.FromVersion, up.ToVersion
			return b
		}
	}
	// digest-drift: does the tag now resolve to bytes the service isn't on?
	reg, err := registryDigest(ctx, sv.Image)
	if err != nil {
		b := with("unknown")
		b.Detail = "registry: " + err.Error()
		return b
	}
	known := sv.Known
	if len(known) == 0 && sv.Running != "" {
		known = []string{sv.Running}
	}
	if len(known) > 0 {
		if hasBytes(ctx, sv.Image, reg, known) {
			return with("current")
		}
		b := with("available")
		b.Latest = reg
		return b
	}
	digests, err := local.ImageRepoDigests(ctx, sv.Image)
	if err != nil {
		b := with("unknown")
		b.Detail = err.Error()
		return b
	}
	if len(digests) == 0 {
		b := with("unknown")
		b.Detail = sv.Image + " is not present locally (the tag was removed or never pulled) -- Update pulls it"
		return b
	}
	// Compare only the digest portion, so repo-name spelling doesn't matter
	// (bare "postgres:16" vs canonical "docker.io/library/postgres@...").
	localDigests := make([]string, 0, len(digests))
	for _, d := range digests {
		if at := strings.LastIndex(d, "@"); at >= 0 {
			localDigests = append(localDigests, d[at+1:])
		}
	}
	if hasBytes(ctx, sv.Image, reg, localDigests) {
		return with("current")
	}
	b := with("available")
	b.Latest = reg
	return b
}

// hasBytes reports whether an image known by these digests already holds what
// the tag resolves to: the tag's index digest itself, or -- when that moved --
// this host's platform manifest inside it. An index changes whenever any
// platform is rebuilt, so on its own it flags an arm64-only rebuild as an
// update on amd64 that pulls nothing new.
func hasBytes(ctx context.Context, image, reg string, known []string) bool {
	if slices.Contains(known, reg) {
		return true
	}
	plat, err := registryPlatform(ctx, image, reg)
	if err != nil || plat == "" {
		return false
	}
	if slices.Contains(known, plat) {
		return true
	}
	// The running image may know only the index it was pulled as -- it learns
	// a new one only when something re-pulls. Its own platform manifest, asked
	// of the registry, answers for an other-arch rebuild nobody has pulled yet.
	was, err := registryPlatform(ctx, image, known[0])
	return err == nil && was == plat
}

// newerVersion reports whether the train containing currentTag has a newer
// pinned version. registry.Trains returns each train newest-first, so a train
// whose newest tag differs from currentTag is an available upgrade.
func newerVersion(ctx context.Context, repo, currentTag string, schemeFor SchemeFor) (Status, bool) {
	var sch *registry.Scheme
	if schemeFor != nil {
		sch = schemeFor(repo)
	}
	trains, err := registryTrains(ctx, repo, sch)
	if err != nil {
		return Status{}, false
	}
	for _, versions := range trains {
		for _, v := range versions {
			if v.Tag != currentTag {
				continue
			}
			if len(versions) > 0 && versions[0].Tag != currentTag {
				return Status{
					State:       "upgrade",
					Tag:         currentTag,
					NewTag:      versions[0].Tag,
					FromVersion: v.Version,
					ToVersion:   versions[0].Version,
				}, true
			}
			return Status{}, false // already the newest in its train
		}
	}
	return Status{}, false
}

// isVersionTag reports whether a tag is a pinned version (digit-leading), as
// opposed to a rolling channel tag (latest, pkg, develop...). Matches how
// registry.discoverTrains classifies pins vs channels.
func isVersionTag(tag string) bool {
	return tag != "" && tag[0] >= '0' && tag[0] <= '9'
}

// imageTag returns an image ref's tag (ignoring any @digest), defaulting to
// "latest" when none is present.
func imageTag(image string) string {
	ref := image
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[colon+1:]
	}
	return "latest"
}
