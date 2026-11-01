package podman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/registry"
	"github.com/daemonless/fjord/pkg/stack"
)

// A pull that does not fit failed halfway, leaving a half-written image and
// a stack that would not start, with podman's error about a full disk at the
// end of a long wait. The registry says what a pull downloads, and the
// storage dataset what is free: so say it first, plainly.

// errNoSpace is a pull refused because the images cannot fit.
var errNoSpace = errors.New("not enough space for the images")

// spaceVerdict says whether images that download as need bytes fit in free:
// refuse when even the download does not fit, warn when it fits but leaves
// less than about two more of it for unpacking (an estimate: unpacked size is
// not known before the pull).
func spaceVerdict(need, free uint64) (refuse, warn bool) {
	switch {
	case need == 0:
		return false, false
	case free < need:
		return true, false
	case free < 3*need:
		return false, true
	}
	return false, false
}

// checkPullSpace refuses (errNoSpace, with an [error] line) or warns before
// pulling images, nil when they fit or nothing can be told: an unknown size
// or free space never blocks a pull.
//
// refuse is false for an update: the image's layers are mostly on the host
// already, so its full size overstates the download, and an update that would
// fit must not be blocked on that. It warns instead.
func (b *Backend) checkPullSpace(ctx context.Context, w io.Writer, images []string, refuse bool) error {
	var need uint64
	for _, img := range images {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		size, _, err := registry.ImageSize(sctx, img)
		cancel()
		if err == nil && size > 0 {
			need += uint64(size)
		}
	}
	if need == 0 {
		return nil
	}
	root, err := b.graphRoot(ctx)
	if err != nil {
		return nil
	}
	free, err := freeBytes(root)
	if err != nil {
		return nil
	}
	full, warn := spaceVerdict(need, free)
	if full && !refuse {
		full, warn = false, true
	}
	switch {
	case full:
		fmt.Fprintf(w, "\n[error] not enough space: the images download as %s and only %s is free on %s -- free some space (Maintenance > Prune removes images nothing uses) and try again\n",
			humanBytes(int64(need)), humanBytes(int64(free)), root)
		return errNoSpace
	case warn:
		fmt.Fprintf(w, "[warn] the images download as %s and grow when unpacked; only %s is free on %s\n",
			humanBytes(int64(need)), humanBytes(int64(free)), root)
	}
	return nil
}

// graphRoot is where podman keeps images, from its own API.
func (b *Backend) graphRoot(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/info", nil)
	if err != nil {
		return "", err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var info struct {
		Store struct {
			GraphRoot string `json:"graphRoot"`
		} `json:"store"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&info) != nil || info.Store.GraphRoot == "" {
		return "", fmt.Errorf("libpod info: no graphRoot")
	}
	return info.Store.GraphRoot, nil
}

// servicesImages are the images of the named services (all when none).
func servicesImages(s *stack.Stack, services []string) []string {
	var out []string
	for _, svc := range composepkg.ParseServices(s.Compose, s.EnvMap()) {
		if svc.Image == "" {
			continue
		}
		if len(services) > 0 {
			found := false
			for _, n := range services {
				found = found || n == svc.Name
			}
			if !found {
				continue
			}
		}
		out = append(out, svc.Image)
	}
	return out
}
