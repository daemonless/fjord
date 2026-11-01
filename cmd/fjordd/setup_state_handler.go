package main

import (
	"encoding/json"
	"net/http"
	"runtime"
	"slices"
	"strings"

	"github.com/daemonless/fjord/pkg/catalog"
)

// The first-run setup wizard runs until it is finished or skipped; the UI
// asks here on load and Settings → Advanced can reset it to run again.

// defaultCatalog is the catalog a fresh install starts with, and Settings
// offers to add back: daemonless's images run on FreeBSD, linuxserver.io's on
// Linux. The store leaves another OS's apps out, so the wrong one is empty.
func defaultCatalog() (id, url string) {
	d := defaultCatalogs(runtime.GOOS)[0]
	return d.ID, d.URL
}

// defaultCatalogs are every catalog a fresh install starts with, the main
// one first. On Linux daemonless's comes too: the store shows only its
// images built for Linux (the reverse proxy, the pkg cache).
func defaultCatalogs(goos string) []catalog.Source {
	daemonless := catalog.Source{ID: "daemonless", Name: "daemonless", URL: "https://catalog.daemonless.io/v1/daemonless"}
	if goos == "linux" {
		return []catalog.Source{{ID: "linuxserver", Name: "linuxserver", URL: "https://catalog.daemonless.io/v1/linuxserver"}, daemonless}
	}
	return []catalog.Source{daemonless}
}

// newDefaults adds the default catalogs this install has not been given yet
// to sources, and returns every default id as given. One that predates
// CatalogDefaults was bootstrapped with the main default only; a catalog
// already there, by id or URL, is not added twice.
func newDefaults(sources []catalog.Source, given []string, bootstrapped bool, defaults []catalog.Source) ([]catalog.Source, []string) {
	if given == nil && bootstrapped {
		given = []string{defaults[0].ID}
	}
	out := slices.Clone(sources)
	for _, d := range defaults {
		if slices.Contains(given, d.ID) {
			continue
		}
		given = append(given, d.ID)
		if !slices.ContainsFunc(out, func(s catalog.Source) bool {
			return s.ID == d.ID || strings.TrimRight(s.URL, "/") == d.URL
		}) {
			out = append(out, d)
		}
	}
	return out, given
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
