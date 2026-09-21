// Package updates detects whether a stack's images are behind what its
// registry tags currently offer. It is runtime-agnostic: local image state
// comes through engine.Backend, registry state through pkg/registry.
package updates

import (
	"context"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/registry"
)

// Status is the result of an update-availability check for a stack. Two kinds
// of "behind" are distinguished because they need different actions:
//   - "available": the current tag's bytes moved upstream -> Update (pull) applies it.
//   - "upgrade":   a newer VERSION exists in the tag's train -> Change Version
//     applies it (Update wouldn't, since the compose still names the old tag).
type Status struct {
	State       string `json:"state"`                 // current | available | upgrade | pinned | unknown
	Tag         string `json:"tag,omitempty"`         // the ref's tag, for display
	Latest      string `json:"latest,omitempty"`      // registry (index) digest, when "available"
	NewTag      string `json:"newTag,omitempty"`      // newer version's tag, when "upgrade"
	FromVersion string `json:"fromVersion,omitempty"` // e.g. "2.16.0", when "upgrade"
	ToVersion   string `json:"toVersion,omitempty"`   // e.g. "2.17.0", when "upgrade"
	Detail      string `json:"detail,omitempty"`      // why the state is "unknown", for the badge tooltip
}

// SchemeFor resolves an image repo to the tag scheme its catalog entry
// declares, or nil when nothing declares one (an adopted stack on a
// third-party image) -- in which case the scheme is inferred from the tags.
type SchemeFor func(repo string) *registry.Scheme

// Check evaluates every service image (Update pulls them all) and aggregates:
// any image with a digest drift => "available"; else any with a newer version
// => "upgrade"; else any not confirmable => "unknown"; else all frozen =>
// "pinned"; else "current". schemeFor may be nil.
func Check(ctx context.Context, backend engine.Backend, images []string, schemeFor SchemeFor) (Status, error) {
	if len(images) == 0 {
		return Status{State: "unknown", Detail: "no images to check"}, nil
	}
	tag := imageTag(images[0]) // representative label for the badge
	anyUnknown, allPinned := false, true
	unknownDetail := ""
	var upgrade *Status
	for _, image := range images {
		es, err := evalImage(ctx, backend, image, schemeFor)
		if err != nil {
			return Status{}, err
		}
		switch es.State {
		case "pinned":
			continue // stays frozen; doesn't clear allPinned
		case "available":
			return es, nil // digest drift, Update-actionable -- highest priority
		case "upgrade":
			allPinned = false
			if upgrade == nil {
				u := es
				upgrade = &u
			}
		case "unknown":
			allPinned = false
			anyUnknown = true
			if unknownDetail == "" {
				unknownDetail = es.Detail
			}
		default: // current
			allPinned = false
		}
	}
	if upgrade != nil {
		return *upgrade, nil
	}
	if anyUnknown {
		return Status{State: "unknown", Tag: tag, Detail: unknownDetail}, nil
	}
	if allPinned {
		return Status{State: "pinned", Tag: tag}, nil
	}
	return Status{State: "current", Tag: tag}, nil
}

// evalImage classifies one service image. Pinned refs are frozen. A version-pin
// tag (digit-leading, e.g. "2.16.0-pkg") is checked against its train for a
// newer release first; otherwise (and for rolling tags) it falls back to a
// digest-drift check against the registry.
func evalImage(ctx context.Context, backend engine.Backend, image string, schemeFor SchemeFor) (Status, error) {
	tag := imageTag(image)
	if strings.Contains(image, "@sha256:") {
		return Status{State: "pinned", Tag: tag}, nil
	}
	if isVersionTag(tag) {
		if up, ok := newerVersion(ctx, registry.Repo(image), tag, schemeFor); ok {
			return up, nil
		}
	}
	// digest-drift: does the tag now resolve to bytes we don't have locally?
	reg, err := registry.Digest(ctx, image)
	if err != nil {
		return Status{}, err
	}
	local, err := backend.ImageRepoDigests(ctx, image)
	if err != nil {
		return Status{}, err
	}
	if len(local) == 0 {
		return Status{State: "unknown", Tag: tag, Detail: image + " is not present locally (the tag was removed or never pulled) -- Update pulls it"}, nil
	}
	// Compare only the digest portion, so repo-name spelling doesn't matter
	// (bare "postgres:16" vs canonical "docker.io/library/postgres@...").
	for _, d := range local {
		if at := strings.LastIndex(d, "@"); at >= 0 && d[at+1:] == reg {
			return Status{State: "current", Tag: tag}, nil
		}
	}
	return Status{State: "available", Tag: tag, Latest: reg}, nil
}

// newerVersion reports whether the train containing currentTag has a newer
// pinned version. registry.Trains returns each train newest-first, so a train
// whose newest tag differs from currentTag is an available upgrade.
func newerVersion(ctx context.Context, repo, currentTag string, schemeFor SchemeFor) (Status, bool) {
	var sch *registry.Scheme
	if schemeFor != nil {
		sch = schemeFor(repo)
	}
	trains, err := registry.Trains(ctx, repo, sch)
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
