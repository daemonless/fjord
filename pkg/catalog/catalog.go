// Package catalog manages the app store's sources: multiple named catalogs
// (each a base URL serving catalog.json + icons/ + manifests/), fetched and
// cached per-source under the fjord root, then merged into one store document
// with every app tagged by its origin catalog. A hand-seeded catalog at the
// cache root (the pre-multi-catalog layout) is honored as a built-in "local"
// source, so offline installs keep working with zero configuration.
//
// Fetched catalog.json files are stored byte-for-byte as published -- all
// rewriting (icon paths, manifest URLs, origin tags) happens at merge time on
// generic maps, so fields this package doesn't know about pass through intact.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// LocalID is the reserved source id for a hand-seeded catalog at the cache
// root. It has no URL: nothing to refresh, deletion means removing the files.
const LocalID = "local"

// Source is one configured catalog: a base URL under which catalog.json,
// icons/<id>.<ext> and manifests/<id>.yaml are published.
type Source struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Disabled bool   `json:"disabled,omitempty"` // configured but excluded from the store
}

// Cache fetches + stores every source's catalog under dir/<source-id>/.
type Cache struct {
	dir    string
	client *http.Client
	mu     sync.RWMutex // refreshes take the write lock; Merged/manifest reads the read lock

	srcMu   sync.RWMutex
	sources []Source
}

func New(dir string, sources []Source) *Cache {
	return &Cache{
		dir:     dir,
		sources: append([]Source(nil), sources...),
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Sources returns the configured sources (not including the local seed).
func (c *Cache) Sources() []Source {
	c.srcMu.RLock()
	defer c.srcMu.RUnlock()
	return append([]Source(nil), c.sources...)
}

// SetSources swaps the configured sources at runtime (the Settings page).
func (c *Cache) SetSources(s []Source) {
	c.srcMu.Lock()
	c.sources = append([]Source(nil), s...)
	c.srcMu.Unlock()
}

func (c *Cache) source(id string) (Source, bool) {
	for _, s := range c.Sources() {
		if s.ID == id {
			return s, true
		}
	}
	return Source{}, false
}

// LocalSeed reports whether a hand-seeded catalog exists at the cache root.
func (c *Cache) LocalSeed() bool {
	_, err := os.Stat(filepath.Join(c.dir, "catalog.json"))
	return err == nil
}

// Available reports whether any catalog (fetched or seeded) is cached.
func (c *Cache) Available() bool {
	if c.LocalSeed() {
		return true
	}
	for _, s := range c.Sources() {
		if _, err := os.Stat(filepath.Join(c.dir, s.ID, "catalog.json")); err == nil {
			return true
		}
	}
	return false
}

// Unfetched returns sources that have no cached catalog yet (fresh adds) --
// the ones worth auto-fetching at startup.
func (c *Cache) Unfetched() []Source {
	var out []Source
	for _, s := range c.Sources() {
		if _, err := os.Stat(filepath.Join(c.dir, s.ID, "catalog.json")); err != nil {
			out = append(out, s)
		}
	}
	return out
}

// RefreshAll re-fetches every configured source; per-source failures are
// joined so one dead catalog doesn't hide the others' success.
func (c *Cache) RefreshAll(ctx context.Context) error {
	var errs []error
	for _, s := range c.Sources() {
		if err := c.RefreshOne(ctx, s.ID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
		}
	}
	return errors.Join(errs...)
}

// RefreshOne re-fetches a single source's catalog.json + icons and clears its
// cached manifests, which are re-fetched lazily on the next install.
func (c *Cache) RefreshOne(ctx context.Context, id string) error {
	src, ok := c.source(id)
	if !ok {
		return fmt.Errorf("no such catalog %q", id)
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	raw, err := c.fetch(ctx, src, "catalog.json")
	if err != nil {
		return fmt.Errorf("fetch catalog.json: %w", err)
	}
	var doc struct {
		Icon string           `json:"icon"` // catalog-level branding icon
		Apps []map[string]any `json:"apps"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse catalog.json: %w", err)
	}

	iconDir := filepath.Join(c.dir, src.ID, "icons")
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		return err
	}

	if doc.Icon != "" {
		if data, ext, ok := c.pullIcon(ctx, src, doc.Icon); ok {
			writeAtomic(filepath.Join(c.dir, src.ID, "icon"+ext), data)
		}
	}
	for _, a := range doc.Apps {
		appID, _ := a["id"].(string)
		icon, _ := a["icon"].(string)
		if appID == "" || icon == "" {
			continue
		}
		data, ext, ok := c.pullIcon(ctx, src, icon)
		if !ok {
			continue // merge falls back to the letter placeholder
		}
		writeAtomic(filepath.Join(iconDir, safeID(appID)+ext), data)
	}

	// Manifests are fetched lazily at install and cached; a refresh must drop
	// them or the wizard keeps serving the manifest from the first install.
	os.RemoveAll(filepath.Join(c.dir, src.ID, "manifests"))

	// The published document is stored verbatim: unknown fields survive, and
	// all path rewriting happens at merge time.
	return writeAtomic(filepath.Join(c.dir, src.ID, "catalog.json"), raw)
}

// Merged builds the store document served at /catalog/catalog.json: one entry
// per app id, with a "sources" array of every catalog that offers it (each
// carrying that catalog's own manifest_url, icon, image, version and
// variants). Catalogs are visited in PRIORITY order -- configured order (top =
// highest), local seed last as a fallback -- and within a group:
//
//   - sources[] is priority-ordered, so the install wizard lists repositories
//     high-to-low and defaults to sources[0];
//   - the default install fields (manifest_url, version, variants, the badge)
//     come from the highest-priority source;
//   - sparse display fields (icon, description, links) COALESCE down the
//     priority list -- the highest-priority source that actually has one wins,
//     so a terse top catalog still borrows a lower one's icon.
func (c *Cache) Merged() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var order []string // first-seen app ids, for stable output
	grouped := map[string]map[string]any{}

	add := func(id, name, dir string) {
		raw, err := os.ReadFile(filepath.Join(dir, "catalog.json"))
		if err != nil {
			return
		}
		var doc struct {
			Apps []map[string]any `json:"apps"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			return
		}
		for _, a := range doc.Apps {
			appID, _ := a["id"].(string)
			if appID == "" {
				continue
			}
			icon, _ := a["icon"].(string)
			manifestURL, _ := a["manifest_url"].(string)
			if id != LocalID {
				icon = c.cachedIcon(id, appID)
				manifestURL = "/catalog/" + id + "/manifests/" + safeID(appID) + ".yaml"
			}
			// Local seed keeps its original /catalog/... paths (that's where
			// its files are); configured sources are scoped by id. Sparse
			// display fields ride along so the group can coalesce them.
			src := map[string]any{
				"catalog":      id,
				"catalog_name": name,
				"catalog_icon": c.CatalogIcon(id),
				"manifest_url": manifestURL,
				"icon":         icon,
				"image":        a["image"],
				"version":      a["version"],
				"variants":     a["variants"],
				"description":  a["description"],
				"upstream_url": a["upstream_url"],
				"web_url":      a["web_url"],
			}
			if existing, ok := grouped[appID]; ok {
				existing["sources"] = append(existing["sources"].([]map[string]any), src)
				continue
			}
			// Highest priority to provide this app: its fields lead, and its
			// own source heads the (priority-ordered) list.
			a["catalog"] = id
			a["catalog_name"] = name
			a["catalog_icon"] = c.CatalogIcon(id)
			a["icon"] = icon
			a["manifest_url"] = manifestURL
			a["sources"] = []map[string]any{src}
			grouped[appID] = a
			order = append(order, appID)
		}
	}

	// Priority order: configured catalogs (as stored, top = highest), then the
	// local seed as the lowest-priority fallback.
	for _, s := range c.Sources() {
		if s.Disabled {
			continue // configured but turned off -- its apps stay out of the store
		}
		add(s.ID, s.Name, filepath.Join(c.dir, s.ID))
	}
	if c.LocalSeed() {
		add(LocalID, "local seed", c.dir)
	}

	// Coalesce sparse display fields down each group's priority-ordered sources.
	for _, id := range order {
		g := grouped[id]
		srcs := g["sources"].([]map[string]any)
		for _, field := range []string{"icon", "description", "upstream_url", "web_url"} {
			if cur, _ := g[field].(string); cur != "" {
				continue // highest-priority already has it
			}
			for _, src := range srcs {
				if v, _ := src[field].(string); v != "" {
					g[field] = v
					break
				}
			}
		}
	}

	apps := make([]map[string]any, 0, len(order))
	for _, id := range order {
		apps = append(apps, grouped[id])
	}
	// Case-insensitive by name, then id: publishers' order is not trusted, and
	// several merged catalogs would otherwise interleave unpredictably.
	sort.SliceStable(apps, func(i, j int) bool {
		a, _ := apps[i]["name"].(string)
		b, _ := apps[j]["name"].(string)
		if la, lb := strings.ToLower(a), strings.ToLower(b); la != lb {
			return la < lb
		}
		ia, _ := apps[i]["id"].(string)
		ib, _ := apps[j]["id"].(string)
		return ia < ib
	})
	return json.Marshal(map[string]any{"apps": apps})
}

// cachedIcon returns the served path of a source app's locally cached icon,
// or "" when the icon fetch failed (UI shows a letter placeholder).
func (c *Cache) cachedIcon(srcID, appID string) string {
	matches, _ := filepath.Glob(filepath.Join(c.dir, srcID, "icons", safeID(appID)+".*"))
	if len(matches) == 0 {
		return ""
	}
	return "/catalog/" + srcID + "/icons/" + filepath.Base(matches[0])
}

// CatalogIcon returns the served path of a source's cached branding icon, or
// "" when it has none. For the local seed that's a root-level icon.<ext>.
func (c *Cache) CatalogIcon(id string) string {
	if id == LocalID {
		matches, _ := filepath.Glob(filepath.Join(c.dir, "icon.*"))
		if len(matches) == 0 {
			return ""
		}
		return "/catalog/" + filepath.Base(matches[0])
	}
	matches, _ := filepath.Glob(filepath.Join(c.dir, safeID(id), "icon.*"))
	if len(matches) == 0 {
		return ""
	}
	return "/catalog/" + safeID(id) + "/" + filepath.Base(matches[0])
}

// FetchedAt is when a source's catalog.json was last written (zero = never).
func (c *Cache) FetchedAt(id string) time.Time {
	dir := c.dir
	if id != LocalID {
		dir = filepath.Join(c.dir, safeID(id))
	}
	fi, err := os.Stat(filepath.Join(dir, "catalog.json"))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// AppCount reports how many apps a source's cached catalog holds (0 = not
// fetched yet). Works for LocalID too.
func (c *Cache) AppCount(id string) int {
	dir := c.dir
	if id != LocalID {
		dir = filepath.Join(c.dir, id)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "catalog.json"))
	if err != nil {
		return 0
	}
	var doc struct {
		Apps []json.RawMessage `json:"apps"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return 0
	}
	return len(doc.Apps)
}

// RemoveSourceFiles deletes a source's cached files (catalog.json, icons,
// manifests). For LocalID that means the root-level seed files only -- never
// the per-source dirs beside them.
func (c *Cache) RemoveSourceFiles(id string) error {
	if id == LocalID {
		var errs []error
		errs = append(errs, os.Remove(filepath.Join(c.dir, "catalog.json")))
		errs = append(errs, os.RemoveAll(filepath.Join(c.dir, "icons")))
		errs = append(errs, os.RemoveAll(filepath.Join(c.dir, "manifests")))
		for _, err := range errs {
			if err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	}
	return os.RemoveAll(filepath.Join(c.dir, safeID(id)))
}

// ManifestAny resolves a manifest by app id across every source (local seed
// first, then configured catalogs), returning the first hit. It backs the
// legacy source-less /catalog/manifests/<app>.yaml route so a browser holding
// a pre-multi-catalog catalog.json (whose manifest_urls lack the source
// segment) can still install.
func (c *Cache) ManifestAny(ctx context.Context, appID string) ([]byte, error) {
	ids := []string{LocalID}
	for _, s := range c.Sources() {
		ids = append(ids, s.ID)
	}
	var lastErr error
	for _, id := range ids {
		if id == LocalID && !c.LocalSeed() {
			continue
		}
		if b, err := c.Manifest(ctx, id, appID); err == nil {
			return b, nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("manifest %q not found in any catalog", appID)
	}
	return nil, lastErr
}

// Manifest returns a stack manifest for an app of the given source, serving
// the cached copy or lazily fetching + caching it on first use. The local
// seed has no URL to fetch from -- cache-only.
func (c *Cache) Manifest(ctx context.Context, srcID, appID string) ([]byte, error) {
	dir := c.dir
	if srcID != LocalID {
		dir = filepath.Join(c.dir, safeID(srcID))
	}
	p := filepath.Join(dir, "manifests", safeID(appID)+".yaml")
	if b, err := os.ReadFile(p); err == nil {
		return b, nil
	}
	src, ok := c.source(srcID)
	if !ok {
		return nil, fmt.Errorf("no such catalog %q", srcID)
	}
	b, err := c.fetch(ctx, src, "manifests/"+safeID(appID)+".yaml")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
		writeAtomic(p, b)
	}
	return b, nil
}

// writeAtomic writes via a temp file + rename so a concurrent reader (Merged,
// the catalog file server) never sees a half-written catalog or icon.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// AppIconPath returns the on-disk path of an app's cached icon from the first
// source that has it, or "" -- for copying into a stack at install.
func (c *Cache) AppIconPath(appID string) string {
	for _, s := range c.Sources() {
		matches, _ := filepath.Glob(filepath.Join(c.dir, s.ID, "icons", safeID(appID)+".*"))
		if len(matches) > 0 {
			return matches[0]
		}
	}
	return ""
}

// FilePath returns the on-disk path for a cached file (merged-doc siblings:
// icons/..., <src>/icons/..., legacy root files).
func (c *Cache) FilePath(rel string) string {
	return filepath.Join(c.dir, filepath.Clean("/"+rel))
}

// LegacyIconPath resolves an old-style source-less "icons/<file>" request
// across the local seed and every configured source, returning the on-disk
// path of the first match (or "" if none). Backs stale browsers holding a
// pre-multi-catalog catalog.json whose icon paths lack a source segment.
func (c *Cache) LegacyIconPath(file string) string {
	file = safeID(file)
	if p := filepath.Join(c.dir, "icons", file); fileExists(p) {
		return p // local seed
	}
	for _, s := range c.Sources() {
		if p := filepath.Join(c.dir, s.ID, "icons", file); fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// pullIcon fetches an app's icon from wherever the catalog points -- an
// absolute URL (Iconify fallback) or a /catalog/icons/... path relative to
// the source.
func (c *Cache) pullIcon(ctx context.Context, src Source, icon string) (data []byte, ext string, ok bool) {
	if strings.HasPrefix(icon, "http") {
		b, err := c.get(ctx, icon)
		return b, ".svg", err == nil
	}
	b, err := c.fetch(ctx, src, strings.TrimPrefix(icon, "/catalog/"))
	return b, path.Ext(icon), err == nil
}

func (c *Cache) fetch(ctx context.Context, src Source, rel string) ([]byte, error) {
	base := strings.TrimRight(src.URL, "/")
	if base == "" {
		return nil, fmt.Errorf("catalog %q has no source URL", src.ID)
	}
	return c.get(ctx, base+"/"+rel)
}

func (c *Cache) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// safeID strips any path components from an id used to build a file path.
func safeID(id string) string {
	return path.Base(filepath.Clean("/" + id))
}

// TagScheme returns the tag scheme an image repo's catalog entry declares:
// every rolling channel tag it publishes (each variant's id, plus that
// variant's aliases), and a mapping from each alias to the channel it follows.
// Both are empty when no catalog entry claims the repo -- an adopted stack on
// a third-party image -- and the caller falls back to inferring the scheme.
//
// The lookup is by image repo, not app id, because a stack's images need not
// all belong to the app that installed it: a sparkyfitness compose pulls
// ghcr.io/daemonless/postgres, whose scheme lives on the postgres entry.
func (c *Cache) TagScheme(repo string) (channels []string, aliases map[string]string) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return nil, nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	scan := func(dir string) bool {
		raw, err := os.ReadFile(filepath.Join(dir, "catalog.json"))
		if err != nil {
			return false
		}
		var doc struct {
			Apps []struct {
				Image    string `json:"image"`
				Variants []struct {
					ID      string   `json:"id"`
					Aliases []string `json:"aliases"`
				} `json:"variants"`
			} `json:"apps"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			return false
		}
		for _, a := range doc.Apps {
			if a.Image != repo || len(a.Variants) == 0 {
				continue
			}
			aliases = map[string]string{}
			for _, v := range a.Variants {
				if v.ID == "" {
					continue
				}
				channels = append(channels, v.ID)
				for _, al := range v.Aliases {
					if al == "" || al == v.ID {
						continue
					}
					channels = append(channels, al)
					aliases[al] = v.ID
				}
			}
			return true
		}
		return false
	}

	// Same priority order as Merged, so the entry that supplies the app's
	// install fields is the one that describes its tags.
	for _, s := range c.Sources() {
		if s.Disabled {
			continue
		}
		if scan(filepath.Join(c.dir, s.ID)) {
			return channels, aliases
		}
	}
	if c.LocalSeed() && scan(c.dir) {
		return channels, aliases
	}
	return nil, nil
}
