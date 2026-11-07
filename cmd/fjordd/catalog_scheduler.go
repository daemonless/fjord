package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// Automatic catalog refresh (Settings → Catalogs). The published catalogs
// rebuild a few times a day, so 6h keeps versions current without polling
// anyone hard; "off" leaves it to the manual Refresh buttons.
var catalogRefreshIntervals = map[string]time.Duration{
	"off": 0,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
}

const catalogRefreshDefault = "6h"

func catalogRefreshOf(st savedSettings) string {
	if _, ok := catalogRefreshIntervals[st.CatalogRefresh]; !ok {
		return catalogRefreshDefault
	}
	return st.CatalogRefresh
}

func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// oldestFetch is the stalest configured source's catalog.json time (zero if
// any source was never fetched, which makes it due immediately).
func (s *server) oldestFetch() time.Time {
	var oldest time.Time
	first := true
	for _, src := range s.cat.Sources() {
		if src.Disabled {
			continue
		}
		t := s.cat.FetchedAt(src.ID)
		if t.IsZero() {
			return time.Time{}
		}
		if first || t.Before(oldest) {
			oldest, first = t, false
		}
	}
	return oldest
}

const catalogRetryBackoff = 15 * time.Minute

func (s *server) catalogAutoDue(interval time.Duration, now time.Time) bool {
	if interval <= 0 || len(s.cat.Sources()) == 0 {
		return false
	}
	oldest := s.oldestFetch()
	if !oldest.IsZero() {
		return now.Sub(oldest) >= interval
	}
	// At least one source has never succeeded (or its catalog.json is absent).
	// Throttle retries by catalogAutoAt so one dead source does not re-fetch
	// every working catalog and wipe cached manifests once per minute.
	s.catalogAutoMu.Lock()
	lastTry := s.catalogAutoAt
	s.catalogAutoMu.Unlock()
	if lastTry.IsZero() {
		return true
	}
	backoff := min(interval, catalogRetryBackoff)
	return now.Sub(lastTry) >= backoff
}

// runCatalogScheduler refreshes every catalog once the stalest one is older
// than the configured interval. Checked each minute so a settings change
// applies without a restart; the interval is measured from the last fetch,
// so a restart never triggers a burst.
func runCatalogScheduler(s *server) {
	for {
		interval := catalogRefreshIntervals[catalogRefreshOf(loadSettings(s.fjordRoot))]
		if s.catalogAutoDue(interval, time.Now()) {
			ctx, cancel := context.WithTimeout(daemonCtx, 3*time.Minute)
			err := s.cat.RefreshAll(ctx)
			cancel()
			s.catalogAutoMu.Lock()
			s.catalogAutoAt = time.Now()
			s.catalogAutoErr = ""
			if err != nil {
				s.catalogAutoErr = err.Error()
				log.Printf("catalog: automatic refresh: %v", err)
			} else {
				log.Printf("catalog: automatic refresh done (%d sources)", len(s.cat.Sources()))
			}
			s.catalogAutoMu.Unlock()
		}
		select {
		case <-daemonCtx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// handleCatalogRefreshSettings reads (GET) or sets (POST {"interval": "6h"})
// the automatic refresh interval, and reports when the last automatic run was
// and when the next is due.
func (s *server) handleCatalogRefreshSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		st := loadSettings(s.fjordRoot)
		interval := catalogRefreshOf(st)
		s.catalogAutoMu.Lock()
		lastAt, lastErr := s.catalogAutoAt, s.catalogAutoErr
		s.catalogAutoMu.Unlock()
		next := ""
		if d := catalogRefreshIntervals[interval]; d > 0 && len(s.cat.Sources()) > 0 {
			next = rfc3339OrEmpty(s.oldestFetch().Add(d))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"interval":  interval,
			"default":   catalogRefreshDefault,
			"options":   []string{"off", "1h", "6h", "24h"},
			"lastRun":   rfc3339OrEmpty(lastAt),
			"lastError": lastErr,
			"nextDue":   next,
		})
	case http.MethodPost:
		var req struct {
			Interval string `json:"interval"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", 400)
			return
		}
		if _, ok := catalogRefreshIntervals[req.Interval]; !ok {
			http.Error(w, "interval must be off, 1h, 6h or 24h", 400)
			return
		}
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.CatalogRefresh = req.Interval }); err != nil {
			http.Error(w, "persist: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"interval": req.Interval})
	default:
		http.Error(w, "method not allowed", 405)
	}
}
