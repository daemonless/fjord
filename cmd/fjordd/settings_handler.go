package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/catalog"
)

// savedSettings is the UI-editable configuration, persisted as settings.json
// in the fjord root. Saved values override the corresponding FJORD_* env vars
// (env bootstraps, the UI is the operator's live control).
type savedSettings struct {
	CatalogURL      string           `json:"catalogURL,omitempty"` // legacy single-source form, migrated on load
	Catalogs        []catalog.Source `json:"catalogs,omitempty"`
	DefaultEngine   string           `json:"defaultEngine,omitempty"`   // "" = first available engine
	DisabledEngines []string         `json:"disabledEngines,omitempty"` // engines the operator turned off (Plugins tab)
	DisabledPlugins []string         `json:"disabledPlugins,omitempty"` // provider plugins turned off (plugins.go); on by default
	AppData         []string         `json:"appData,omitempty"`         // App data folders, first = default ("" = <fjordRoot>/containers)
	StorageBase     string           `json:"storageBase,omitempty"`     // legacy single App data folder, migrated into AppData on load
	FolderSets      []FolderSet      `json:"folderSets,omitempty"`      // named host-folder sets offered at install
	Libraries       []FolderSet      `json:"libraries,omitempty"`       // legacy name for FolderSets, migrated on load
	WizardDetail    int              `json:"wizardDetail,omitempty"`    // install wizard disclosure: 1 essentials (default), 2 +Options, 3 +Advanced
	// DefaultNetwork is what new installs start on. Kept as the value for
	// engines with no entry of their own -- and as what older fjords wrote.
	DefaultNetwork string `json:"defaultNetwork,omitempty"`
	// DefaultNetworkFor is per engine, because a network can belong to one:
	// appjail cannot attach to podman's private network and vice versa, and a
	// LAN network may be filled in for one engine. One global default meant
	// installing on the other engine silently fell back to nothing.
	DefaultNetworkFor map[string]string `json:"defaultNetworkFor,omitempty"`
	CatalogRefresh    string            `json:"catalogRefresh,omitempty"` // automatic catalog refresh: off | 1h | 6h (default) | 24h
	SetupDone         bool              `json:"setupDone,omitempty"`      // first-run setup wizard finished (or skipped)
	// The default catalog was added once on first run. Never re-added after
	// that, so removing it in Settings sticks across restarts.
	CatalogBootstrapped bool `json:"catalogBootstrapped,omitempty"`
}

func settingsPath(fjordRoot string) string {
	return filepath.Join(fjordRoot, "settings.json")
}

// loadSettings returns the saved settings, migrating the legacy single
// catalogURL into a one-entry catalog list. Zero values on a fresh install or
// unreadable file -- config falls back to env.
func loadSettings(fjordRoot string) savedSettings {
	var s savedSettings
	b, err := os.ReadFile(settingsPath(fjordRoot))
	if err != nil {
		return s
	}
	if err := json.Unmarshal(b, &s); err != nil {
		log.Printf("settings: ignoring unparseable %s: %v", settingsPath(fjordRoot), err)
		return savedSettings{}
	}
	if s.CatalogURL != "" && len(s.Catalogs) == 0 {
		s.Catalogs = []catalog.Source{{ID: "default", Name: "default", URL: s.CatalogURL}}
	}
	s.CatalogURL = ""
	if s.StorageBase != "" && len(s.AppData) == 0 {
		s.AppData = []string{s.StorageBase}
	}
	s.StorageBase = ""
	if len(s.Libraries) > 0 && len(s.FolderSets) == 0 {
		s.FolderSets = s.Libraries
	}
	s.Libraries = nil
	return s
}

// settingsMu serializes read-modify-write cycles: two handlers saving
// different fields at once must not lose each other's change.
var settingsMu sync.Mutex

// saveSettings writes settings.json atomically (temp file + rename in the same
// dir) so a crash mid-write can't leave a truncated file that loads as empty
// and then gets re-saved as empty, wiping catalogs, engines and folder sets.
func saveSettings(fjordRoot string, s savedSettings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := settingsPath(fjordRoot)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
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

// updateSettings load-modifies-saves settings.json so one field's write never
// clobbers the others (catalogs vs default engine live in the same file).
func updateSettings(fjordRoot string, mutate func(*savedSettings)) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s := loadSettings(fjordRoot)
	mutate(&s)
	return saveSettings(fjordRoot, s)
}

// catalogInfo is the wire format of one catalog row in Settings.
type catalogInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Apps      int    `json:"apps"`
	Enabled   bool   `json:"enabled"`             // false = configured but excluded from the store
	Icon      string `json:"icon,omitempty"`      // cached branding icon, served under /catalog/
	Builtin   bool   `json:"builtin,omitempty"`   // the local seed: no URL, nothing to refresh
	FetchedAt string `json:"fetchedAt,omitempty"` // RFC3339, empty = never fetched
}

// handleCatalogs is the catalogs collection: GET lists configured sources
// (plus the local seed when present), POST adds one and fetches it.
func (s *server) handleCatalogs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Listed in priority order: configured catalogs (top = highest), then
		// the local seed as the lowest-priority fallback.
		out := []catalogInfo{}
		for _, src := range s.cat.Sources() {
			out = append(out, catalogInfo{
				ID: src.ID, Name: src.Name, URL: src.URL, Enabled: !src.Disabled,
				Apps: s.cat.AppCount(src.ID), Icon: s.cat.CatalogIcon(src.ID),
				FetchedAt: rfc3339OrEmpty(s.cat.FetchedAt(src.ID)),
			})
		}
		if s.cat.LocalSeed() {
			out = append(out, catalogInfo{
				ID: catalog.LocalID, Name: "local seed", Builtin: true, Enabled: true,
				Apps: s.cat.AppCount(catalog.LocalID), Icon: s.cat.CatalogIcon(catalog.LocalID),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)

	case http.MethodPost:
		var req struct{ Name, URL string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", 400)
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		req.URL = strings.TrimRight(strings.TrimSpace(req.URL), "/")
		if req.Name == "" {
			http.Error(w, "catalog name required", 400)
			return
		}
		if u, err := url.Parse(req.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			http.Error(w, "catalog URL must be http(s)://...", 400)
			return
		}
		sources := s.cat.Sources()
		id := slugify(req.Name)
		if id == "" || id == catalog.LocalID {
			http.Error(w, "invalid catalog name", 400)
			return
		}
		for _, src := range sources {
			if src.ID == id {
				http.Error(w, "a catalog with that name already exists", 409)
				return
			}
		}
		sources = append(sources, catalog.Source{ID: id, Name: req.Name, URL: req.URL})
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.Catalogs = sources }); err != nil {
			http.Error(w, "persist: "+err.Error(), 500)
			return
		}
		s.cat.SetSources(sources)
		// Fetch synchronously: adding a catalog whose URL is wrong should fail
		// the add visibly, not log to a file nobody watches.
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		if err := s.cat.RefreshOne(ctx, id); err != nil {
			http.Error(w, "added, but fetch failed: "+err.Error(), 502)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(catalogInfo{ID: id, Name: req.Name, URL: req.URL, Apps: s.cat.AppCount(id)})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleCatalogRoutes handles one catalog: POST /api/catalogs/<id>/refresh
// and DELETE /api/catalogs/<id> (which also removes its cached files; for the
// local seed that's the only thing deletion means).
func (s *server) handleCatalogRoutes(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/catalogs/"), "/")
	id := parts[0]

	switch {
	case parts[0] == "reorder" && r.Method == http.MethodPost:
		// Reorder catalogs = set priority (top = highest). Drives the merged
		// icon coalesce and the install wizard's repository order/default.
		var req struct {
			Order []string `json:"order"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", 400)
			return
		}
		bySrc := map[string]catalog.Source{}
		for _, s := range s.cat.Sources() {
			bySrc[s.ID] = s
		}
		var reordered []catalog.Source
		for _, id := range req.Order { // requested order first
			if src, ok := bySrc[id]; ok {
				reordered = append(reordered, src)
				delete(bySrc, id)
			}
		}
		for _, s := range s.cat.Sources() { // any omitted keep their relative order at the end
			if src, ok := bySrc[s.ID]; ok {
				reordered = append(reordered, src)
			}
		}
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.Catalogs = reordered }); err != nil {
			http.Error(w, "persist: "+err.Error(), 500)
			return
		}
		s.cat.SetSources(reordered)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"reordered"}`))

	case len(parts) == 2 && parts[1] == "refresh" && r.Method == http.MethodPost:
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		if err := s.cat.RefreshOne(ctx, id); err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(catalogInfo{ID: id, Apps: s.cat.AppCount(id)})

	case len(parts) == 2 && parts[1] == "toggle" && r.Method == http.MethodPost:
		// Enable/disable: keep the catalog configured+cached but include or exclude
		// its apps from the store (Merged skips disabled sources).
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", 400)
			return
		}
		sources := s.cat.Sources()
		found := false
		for i := range sources {
			if sources[i].ID == id {
				sources[i].Disabled = !req.Enabled
				found = true
			}
		}
		if !found {
			http.Error(w, "no such catalog", 404)
			return
		}
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.Catalogs = sources }); err != nil {
			http.Error(w, "persist: "+err.Error(), 500)
			return
		}
		s.cat.SetSources(sources)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(catalogInfo{ID: id, Enabled: req.Enabled, Apps: s.cat.AppCount(id)})

	case len(parts) == 1 && r.Method == http.MethodPost:
		// Rename: change the display name only; the id (cache dir, app badges)
		// stays stable, so no re-fetch is needed.
		var req struct{ Name string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", 400)
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			http.Error(w, "catalog name required", 400)
			return
		}
		sources := s.cat.Sources()
		found := false
		for i := range sources {
			if sources[i].ID == id {
				sources[i].Name = name
				found = true
			}
		}
		if !found {
			http.Error(w, "no such catalog", 404)
			return
		}
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.Catalogs = sources }); err != nil {
			http.Error(w, "persist: "+err.Error(), 500)
			return
		}
		s.cat.SetSources(sources)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "renamed", "name": name})

	case len(parts) == 1 && r.Method == http.MethodDelete:
		if id != catalog.LocalID {
			var kept []catalog.Source
			found := false
			for _, src := range s.cat.Sources() {
				if src.ID == id {
					found = true
					continue
				}
				kept = append(kept, src)
			}
			if !found {
				http.Error(w, "no such catalog", 404)
				return
			}
			// A deliberate removal must stick: an empty list is never "fresh
			// install" again after this.
			if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.Catalogs = kept; st.CatalogBootstrapped = true }); err != nil {
				http.Error(w, "persist: "+err.Error(), 500)
				return
			}
			s.cat.SetSources(kept)
		}
		if err := s.cat.RemoveSourceFiles(id); err != nil {
			http.Error(w, "remove cache: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"deleted"}`))

	default:
		http.Error(w, "not found", 404)
	}
}

// slugify reduces a display name to a filesystem/URL-safe id.
func slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
