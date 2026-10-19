package main

import (
	"encoding/json"
	"net/http"
	"runtime"
)

// The first-run setup wizard runs until it is finished or skipped; the UI
// asks here on load and Settings → Advanced can reset it to run again.

// defaultCatalog is the catalog a fresh install starts with, and Settings
// offers to add back: daemonless's images run on FreeBSD, linuxserver.io's on
// Linux. The store leaves another OS's apps out, so the wrong one is empty.
func defaultCatalog() (id, url string) {
	if runtime.GOOS == "linux" {
		return "linuxserver", "https://catalog.daemonless.io/v1/linuxserver"
	}
	return "daemonless", "https://catalog.daemonless.io/v1/daemonless"
}

func (s *server) handleSetupState(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		id, url := defaultCatalog()
		json.NewEncoder(w).Encode(map[string]any{
			"done":               loadSettings(s.fjordRoot).SetupDone,
			"defaultCatalog":     url,
			"defaultCatalogName": id,
		})
	case http.MethodPost:
		var req struct {
			Done bool `json:"done"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", 400)
			return
		}
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.SetupDone = req.Done }); err != nil {
			http.Error(w, "persist: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"done": req.Done})
	default:
		http.Error(w, "method not allowed", 405)
	}
}
