package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
)

// handleStorageSettings reads (GET) or sets (POST) the App data locations --
// the ordered list of "<base>/<slug>/<name>" roots for app config/data. The
// first is the default; with more than one, the install wizard offers a pick.
// Only affects NEW installs; existing stacks keep their recorded paths.
func (s *server) handleStorageSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		locs := s.appDataLocations()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"locations": locs,
			"base":      locs[0],                                  // default (first)
			"default":   filepath.Join(s.fjordRoot, "containers"), // fallback when nothing is configured
		})

	case http.MethodPost:
		var req struct {
			Locations []string `json:"locations"`
			Base      string   `json:"base"` // legacy single-folder form
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", 400)
			return
		}
		if len(req.Locations) == 0 && strings.TrimSpace(req.Base) != "" {
			req.Locations = []string{req.Base}
		}
		// Normalize: trim, drop blanks + duplicates, require absolute paths.
		// An empty list resets to the built-in default.
		seen := map[string]bool{}
		clean := make([]string, 0, len(req.Locations))
		for _, l := range req.Locations {
			if l = strings.TrimSpace(l); l != "/" {
				l = strings.TrimRight(l, "/")
			}
			if l == "" || seen[l] {
				continue
			}
			if !filepath.IsAbs(l) {
				http.Error(w, "App data locations must be absolute paths: "+l, 400)
				return
			}
			if why := s.unsafeDataLocation(l); why != "" {
				http.Error(w, "not an App data location: "+why+". Pick a folder of its own, like /containers", 400)
				return
			}
			seen[l] = true
			clean = append(clean, l)
		}
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.AppData = clean }); err != nil {
			http.Error(w, "persist: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"locations": s.appDataLocations()})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
