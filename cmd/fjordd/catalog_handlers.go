package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/registry"
)

// handleCatalogFiles serves the merged store document, cached icons, and
// lazily fetched per-source manifests:
//
//	/catalog/catalog.json                 merged doc, apps tagged by source
//	/catalog/<src>/manifests/<app>.yaml   lazy fetch+cache from that source
//	/catalog/manifests/<app>.yaml         local-seed manifest (cache-only)
//	everything else                       static cache files (icons)
func (s *server) handleCatalogFiles(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/catalog/")
	if rel == "catalog.json" {
		b, err := s.cat.Merged()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// Never cache the merged doc: it's cheap to rebuild and a stale copy
		// pins clients to old manifest_urls (source-less, pre-multi-catalog),
		// which then 404 on install.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
		return
	}
	// Catalog assets (icons, manifests) can change in place on a refresh while
	// keeping the same URL, so make the browser revalidate rather than serve a
	// heuristically-"fresh" stale copy (ServeFile's Last-Modified makes the
	// revalidation a cheap 304). Without this a changed icon.svg never shows.
	w.Header().Set("Cache-Control", "no-cache")
	// Icons come from third-party catalogs; an SVG opened directly would run
	// its scripts same-origin. Sandbox it and forbid type sniffing.
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if parts := strings.Split(rel, "/"); len(parts) == 3 && parts[1] == "manifests" {
		b, err := s.cat.Manifest(r.Context(), parts[0], strings.TrimSuffix(parts[2], ".yaml"))
		if err != nil {
			http.Error(w, "manifest not found", 404)
			return
		}
		w.Header().Set("Content-Type", "text/yaml")
		w.Write(b)
		return
	}
	if id, ok := strings.CutPrefix(rel, "manifests/"); ok {
		// Legacy source-less path: resolve across all catalogs so a stale
		// catalog.json (cached before manifest_urls carried a source segment)
		// still installs.
		b, err := s.cat.ManifestAny(r.Context(), strings.TrimSuffix(id, ".yaml"))
		if err != nil {
			http.Error(w, "manifest not found", 404)
			return
		}
		w.Header().Set("Content-Type", "text/yaml")
		w.Write(b)
		return
	}
	// Legacy source-less icon path (stale catalog.json): resolve across
	// sources so a browser holding old icon paths still renders them.
	if file, ok := strings.CutPrefix(rel, "icons/"); ok {
		if p := s.cat.LegacyIconPath(file); p != "" {
			http.ServeFile(w, r, p)
			return
		}
	}
	http.ServeFile(w, r, s.cat.FilePath(rel))
}

// handleCatalogRefresh re-pulls every configured catalog + icons.
func (s *server) handleCatalogRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if err := s.cat.RefreshAll(ctx); err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"refreshed"}`))
}

// handleRegistryVersions lists an image's published versions grouped into
// release trains (auto-discovered from the tags), so the wizard can offer
// train + version. Cached per image (registry.TrainsTTL): every wizard open
// would otherwise be a fresh anonymous tags/list call against ghcr's per-IP limit.
func (s *server) handleRegistryVersions(w http.ResponseWriter, r *http.Request) {
	image := r.URL.Query().Get("image")
	if image == "" {
		http.Error(w, "image query param required", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	trains, err := registry.CachedTrains(ctx, image, s.schemeFor(registry.Repo(image)))
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(trains)
}
