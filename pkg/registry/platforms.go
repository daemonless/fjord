package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"
)

// HostPlatform is this host as an image index names it: "freebsd/amd64".
func HostPlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// Platforms is what an image tag is built for, as "os/arch" strings: every
// entry of a multi-arch index, or the one platform of a plain manifest (read
// from its config blob, which is where a single-arch build records it).
//
// Podman pulls a tag with no build for this host anyway -- one WARNING line,
// then a container that cannot run. army pulled pkg-cache 1.0, an amd64-only
// manifest, onto arm64 that way. This is asked before the tag is chosen.
//
// One GET for an index, two for a plain manifest, so it is asked per tag
// picked, never for a whole tag list: Docker Hub counts these against its
// anonymous pull quota.
func Platforms(ctx context.Context, image, tag string) ([]string, error) {
	body, _, err := Manifest(ctx, image, tag, manifestAccept)
	if err != nil {
		return nil, err
	}
	var m struct {
		Manifests []indexEntry `json:"manifests"`
		Config    struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("decode manifest for %s:%s: %w", image, tag, err)
	}
	seen := map[string]bool{}
	if len(m.Manifests) > 0 {
		for _, e := range m.Manifests {
			// An attestation rides in the index as platform unknown/unknown.
			if e.Platform.OS == "" || e.Platform.OS == "unknown" {
				continue
			}
			seen[e.Platform.OS+"/"+e.Platform.Architecture] = true
		}
	} else if m.Config.Digest != "" {
		cfg, err := Blob(ctx, image, m.Config.Digest, 1<<20)
		if err != nil {
			return nil, err
		}
		var c struct {
			OS   string `json:"os"`
			Arch string `json:"architecture"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, fmt.Errorf("decode image config for %s:%s: %w", image, tag, err)
		}
		if c.OS != "" {
			seen[c.OS+"/"+c.Arch] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// RunsHere says whether a platform list includes this host. An empty list is
// a manifest that named no platform at all, which is not a refusal.
func RunsHere(platforms []string) bool {
	if len(platforms) == 0 {
		return true
	}
	for _, p := range platforms {
		if p == HostPlatform() {
			return true
		}
	}
	return false
}

// PlatformsTTL bounds how stale a cached platform list may be. A tag's
// builds change when CI publishes, like its tag list.
const PlatformsTTL = TrainsTTL

var (
	platformsFn  = Platforms // swapped in tests
	platformsMu  sync.Mutex
	platformsMap = map[string]platformsEntry{}
)

type platformsEntry struct {
	at        time.Time
	platforms []string
}

// CachedPlatforms is Platforms memoized per image:tag for PlatformsTTL. Errors
// are not cached, so a registry hiccup is retried on the next call.
func CachedPlatforms(ctx context.Context, image, tag string) ([]string, error) {
	key := image + ":" + tag
	platformsMu.Lock()
	e, ok := platformsMap[key]
	platformsMu.Unlock()
	if ok && nowFn().Sub(e.at) < PlatformsTTL {
		return e.platforms, nil
	}
	ps, err := platformsFn(ctx, image, tag)
	if err != nil {
		return nil, err
	}
	platformsMu.Lock()
	platformsMap[key] = platformsEntry{at: nowFn(), platforms: ps}
	platformsMu.Unlock()
	return ps, nil
}
