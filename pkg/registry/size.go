package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
)

// ImageSize is what pulling image onto this host downloads: the compressed
// size of its layers and how many there are, from the registry's manifest
// for this host's platform. podman reports no byte progress for a pull --
// not on a pipe, not through its API -- so this is the one number fjord can
// put in front of a pull that is otherwise silent for minutes.
//
// A GET of the manifest (and of the index first, for a multi-arch tag), so
// it counts against Docker Hub's anonymous quota like a pull does: ask once
// per pull, never in a loop.
func ImageSize(ctx context.Context, image string) (bytes int64, layers int, err error) {
	body, media, err := Manifest(ctx, image, imageRef(image), manifestAccept)
	if err != nil {
		return 0, 0, err
	}
	if strings.Contains(media, "index") || strings.Contains(media, "manifest.list") {
		var idx struct {
			Manifests []indexEntry `json:"manifests"`
		}
		if err := json.Unmarshal(body, &idx); err != nil {
			return 0, 0, fmt.Errorf("decode index: %w", err)
		}
		dg := pickPlatform(idx.Manifests, runtime.GOOS, runtime.GOARCH)
		if dg == "" {
			return 0, 0, fmt.Errorf("%s has no %s/%s image", image, runtime.GOOS, runtime.GOARCH)
		}
		if body, _, err = Manifest(ctx, image, dg, manifestAccept); err != nil {
			return 0, 0, err
		}
	}
	var m struct {
		Layers []struct {
			Size int64 `json:"size"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return 0, 0, fmt.Errorf("decode manifest: %w", err)
	}
	for _, l := range m.Layers {
		bytes += l.Size
	}
	return bytes, len(m.Layers), nil
}

// imageRef is the tag or digest an image ref names, "latest" when neither.
func imageRef(image string) string {
	if at := strings.LastIndex(image, "@"); at >= 0 {
		return image[at+1:]
	}
	if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
		return image[colon+1:]
	}
	return "latest"
}
