package main

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"sync"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/registry"
)

// What an install downloads, said in the wizard before Install: per variant
// and per choice, since the images follow both (Immich with machine learning
// pulls one more), and what this host already has.

// imageSize is one image an install pulls.
type imageSize struct {
	Image string `json:"image"`
	Bytes int64  `json:"bytes,omitempty"` // the download, from the registry
	Local bool   `json:"local,omitempty"` // already on this host: nothing to pull
	// Unknown: the registry did not say (private, unreachable, no image for
	// this platform). Never counted, never blocks anything.
	Unknown bool `json:"unknown,omitempty"`
}

type installSizeResponse struct {
	Images   []imageSize `json:"images"`
	Download int64       `json:"download"` // the images not on the host, that answered
}

// installImages are the images an install with these answers pulls,
// resolved as install resolves them up to the point the images are known:
// the choices, then the values and defaults, then the picked tag. A form
// still being filled in is fine: nothing here needs a required value.
func installImages(req installRequest, appImage string) ([]string, error) {
	m, err := manifest.Parse(req.Manifest)
	if err != nil {
		return nil, err
	}
	vals := maps.Clone(req.Values)
	if vals == nil {
		vals = map[string]string{}
	}
	// What a picked option asks for (Your own database's host) is required
	// at install, but never names an image: a form not filled in yet still
	// has a size.
	for _, c := range m.Choices {
		id := cmp.Or(req.Choices[c.ID], c.Default)
		for _, o := range c.Options {
			if o.ID != id {
				continue
			}
			for _, a := range o.Ask {
				if vals[a.Name] == "" {
					vals[a.Name] = "-"
				}
			}
		}
	}
	if _, err := m.ApplyChoices(req.Choices, vals); err != nil {
		return nil, err
	}
	env := maps.Clone(vals)
	for _, v := range m.Variables {
		if env[v.Name] == "" {
			env[v.Name] = composepkg.ExpandEnv(v.Default, nil)
		}
	}
	compose := m.Compose()
	if req.Tag != "" && appImage != "" {
		if compose, err = composepkg.SetRepoTag(compose, appImage, req.Tag); err != nil {
			return nil, err
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, svc := range composepkg.ParseServices(compose, env) {
		if svc.Image != "" && !seen[svc.Image] {
			seen[svc.Image] = true
			out = append(out, svc.Image)
		}
	}
	return out, nil
}

// Registry sizes, kept ten minutes: the wizard asks again on every change of
// choice or tag, and a size does not change between those.
var (
	sizeMu    sync.Mutex
	sizeCache = map[string]sizeEntry{}
)

type sizeEntry struct {
	bytes int64
	ok    bool
	at    time.Time
}

const sizeTTL = 10 * time.Minute

var registryImageSize = registry.ImageSize // swapped by tests

func cachedImageSize(ctx context.Context, image string) (int64, bool) {
	sizeMu.Lock()
	e, hit := sizeCache[image]
	sizeMu.Unlock()
	if hit && time.Since(e.at) < sizeTTL {
		return e.bytes, e.ok
	}
	sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	n, _, err := registryImageSize(sctx, image)
	e = sizeEntry{bytes: n, ok: err == nil && n > 0, at: time.Now()}
	sizeMu.Lock()
	sizeCache[image] = e
	sizeMu.Unlock()
	return e.bytes, e.ok
}

// handleInstallSize: POST /api/apps/size with the install's own body (the
// fields that pick images: manifest, app_id, tag, values, choices, engine).
func (s *server) handleInstallSize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req installRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	images, err := installImages(req, s.cat.AppImage(req.AppID))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	be, haveEngine := s.backend(cmp.Or(req.Engine, s.defaultEngine()))
	resp := installSizeResponse{Images: make([]imageSize, len(images))}
	var wg sync.WaitGroup
	for i, img := range images {
		wg.Add(1)
		go func() {
			defer wg.Done()
			is := imageSize{Image: img}
			if haveEngine {
				lctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				digests, err := be.ImageRepoDigests(lctx, img)
				cancel()
				is.Local = err == nil && len(digests) > 0
			}
			var ok bool
			is.Bytes, ok = cachedImageSize(r.Context(), img)
			is.Unknown = !ok
			resp.Images[i] = is
		}()
	}
	wg.Wait()
	for _, is := range resp.Images {
		if !is.Local && !is.Unknown {
			resp.Download += is.Bytes
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
