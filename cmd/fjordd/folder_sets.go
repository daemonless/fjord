package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Folder sets (the homelab "provider" plugin). A FolderSet is a named set of
// host folders the user can drop into any host-path field at install. fjord
// never guesses which app wants which set -- no variable-name or mount-point
// conventions, nothing catalog-specific -- the user picks it and the wizard's
// folder list does the rest. Design: ~/src/fjord-homelab-plugin.md.
type FolderSet struct {
	ID      string   `json:"id"`   // stable slug from the name
	Name    string   `json:"name"` // display name (Movies, TV, ...)
	Folders []string `json:"folders"`
	// Match lists keywords ("MOVIE|FILM"); a host-path variable whose name
	// contains one gets this set's folders as its default at install. Data,
	// not code: presets seed it, custom sets can leave it empty (manual only).
	Match string `json:"match,omitempty"`
}

// UnmarshalJSON also reads the earlier "library" shape (label + backings[].host)
// so an existing settings.json keeps working and migrates on the next save.
func (l *FolderSet) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Folders  []string `json:"folders"`
		Match    string   `json:"match"`
		Label    string   `json:"label"`
		Backings []struct {
			Host string `json:"host"`
		} `json:"backings"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	l.ID, l.Name, l.Folders, l.Match = raw.ID, raw.Name, raw.Folders, raw.Match
	if l.Name == "" {
		l.Name = raw.Label
	}
	if len(l.Folders) == 0 {
		for _, bk := range raw.Backings {
			if bk.Host != "" {
				l.Folders = append(l.Folders, bk.Host)
			}
		}
	}
	return nil
}

// folderSetPreset is a one-click set the UI offers: a name plus the variable
// keywords it applies to. Any name is allowed for custom sets. A keyword is a
// whole word of the variable name (split at "_"), alone or with an S -- see
// ui/src/folderMatch.ts -- so Audiobooks and Ebooks can be told apart.
type folderSetPreset struct {
	Name  string `json:"name"`
	Match string `json:"match"`
}

// Alphabetical: every screen lists them in this order.
var folderSetPresets = []folderSetPreset{
	{"Audiobooks", "AUDIOBOOK"},
	{"Downloads", "DOWNLOAD"},
	{"Ebooks", "EBOOK|BOOK"},
	{"Movies", "MOVIE|FILM"},
	{"Music", "MUSIC"},
	{"Photos", "PHOTO|PICTURE"},
	{"TV", "TV|SHOW|SERIES"},
}

// handleFolderSets lists (GET) or replaces (PUT) all folder sets. Whole-list
// replace keeps the UI simple: it edits locally and saves.
func (s *server) handleFolderSets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Folder sets are core. The homelab plugin owns the presets (Movies,
		// TV, ...) and the automatic pick-up: with it off no presets show and
		// Match is hidden (kept in settings.json for when it's back on).
		sets := loadSettings(s.fjordRoot).FolderSets
		presets := folderSetPresets
		if !s.pluginEnabled("homelab") {
			presets = []folderSetPreset{}
			plain := make([]FolderSet, len(sets))
			for i, fs := range sets {
				fs.Match = ""
				plain[i] = fs
			}
			sets = plain
		}
		if sets == nil {
			sets = []FolderSet{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"sets":    sets,
			"presets": presets,
		})

	case http.MethodPut, http.MethodPost:
		var req struct {
			Sets []FolderSet `json:"sets"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", 400)
			return
		}
		// Normalize: trim, dedupe folders, derive a stable id, drop empties.
		// The name is the set's identity (the wizard lists sets by name), so
		// two sets can't share one.
		// With the plugin off the UI never sees Match, so a save would drop it;
		// carry the stored value across by id.
		keepMatch := map[string]string{}
		if !s.pluginEnabled("homelab") {
			for _, fs := range loadSettings(s.fjordRoot).FolderSets {
				keepMatch[fs.ID] = fs.Match
			}
		}
		clean := make([]FolderSet, 0, len(req.Sets))
		names := map[string]bool{}
		for _, lib := range req.Sets {
			lib.Name = strings.TrimSpace(lib.Name)
			if lib.Name == "" {
				continue
			}
			if lib.Match == "" {
				lib.Match = keepMatch[lib.ID]
			}
			lib.Match = strings.ToUpper(strings.Join(strings.Fields(strings.ReplaceAll(lib.Match, ",", "|")), ""))
			key := strings.ToLower(lib.Name)
			if names[key] {
				http.Error(w, "two folder sets are named "+lib.Name+" -- give one a different name or merge their folders", http.StatusConflict)
				return
			}
			names[key] = true
			seen := map[string]bool{}
			folders := make([]string, 0, len(lib.Folders))
			for _, f := range lib.Folders {
				f = strings.TrimSpace(f)
				if f == "" || seen[f] {
					continue
				}
				seen[f] = true
				folders = append(folders, f)
			}
			if lib.ID == "" {
				lib.ID = storageSlug(lib.Name, "")
			}
			if lib.ID == "" {
				continue // name had no usable characters
			}
			lib.Folders = folders
			clean = append(clean, lib)
		}
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.FolderSets = clean }); err != nil {
			http.Error(w, "persist: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"sets": clean})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
