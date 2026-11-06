package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/doctor"
)

// Condition is something wrong right now on the host, engine, or network,
// detected by fjord and cleared automatically when no longer true.
type Condition struct {
	ID          string   `json:"id"`
	Severity    string   `json:"severity"` // "critical" | "warning"
	Scope       string   `json:"scope"`    // "host" | "engine" | "network"
	Engine      string   `json:"engine,omitempty"`
	Title       string   `json:"title"`
	Detail      string   `json:"detail,omitempty"`
	Why         string   `json:"why,omitempty"`
	Action      string   `json:"action,omitempty"`
	Commands    []string `json:"commands,omitempty"`
	Fix         string   `json:"fix,omitempty"`
	Installable bool     `json:"installable,omitempty"`
	Since       string   `json:"since"`
}

// conditionsState caches the active conditions from the background doctor loop
// so GET /api/conditions never runs probes on a read path.
type conditionsState struct {
	mu        sync.Mutex
	active    []Condition
	since     map[string]time.Time
	checkedAt time.Time
	pkgMod    time.Time
	wake      chan struct{}
}

func newConditionsState() *conditionsState {
	return &conditionsState{
		active: []Condition{},
		since:  map[string]time.Time{},
		wake:   make(chan struct{}, 1),
	}
}

// conditionScope maps a doctor check to its scope ("host", "engine", "network").
func conditionScope(r doctor.Result) string {
	if r.Group == "networking" {
		return "network"
	}
	if r.Engine != "" {
		return "engine"
	}
	return "host"
}

// conditionTitle gives a human-readable sentence for the condition banner.
func conditionTitle(r doctor.Result) string {
	switch r.ID {
	case "podman-stale":
		return "Podman's API service is running an old version"
	case "socket":
		return "Podman's API socket is not answering"
	case "fjordd-outdated":
		return "fjordd was updated on disk and needs a restart"
	case "hostname-taken":
		return "This host's name resolves to another address in DNS"
	case "storage-low":
		return "Free disk space on the storage root is low"
	case "appjail-dns":
		return "AppJail jail name resolution is not answering"
	case "dnsname":
		return "Container name resolution (dnsname) is not installed"
	case "fdescfs":
		return "fdescfs is not mounted on /dev/fd"
	case "pf":
		return "Podman's pf firewall anchors are not active"
	case "appjail-pf":
		return "AppJail's pf firewall anchors are not active"
	case "dhcp-default":
		return "Host DHCP is running on every container interface"
	}
	return r.Name
}

// conditionsFromChecks turns a doctor report into sorted active conditions,
// keeping the first-seen timestamp for any condition that stays active across
// runs and dropping timestamps for conditions that cleared.
func conditionsFromChecks(checks []doctor.Result, since map[string]time.Time, now time.Time) ([]Condition, map[string]time.Time) {
	nextSince := make(map[string]time.Time, len(checks))
	out := make([]Condition, 0)
	for _, c := range checks {
		if c.Optional {
			continue
		}
		if c.Status != doctor.Fail && c.Status != doctor.Warn {
			continue
		}
		// "appjail" withEngine=="" warns when pre-5.5 or missing director on a
		// host that may not use AppJail; only report it when AppJail is an
		// active engine, which the caller filters via Config.Engines.
		sev := "warning"
		if c.Status == doctor.Fail {
			sev = "critical"
		}
		t, ok := since[c.ID]
		if !ok || t.IsZero() {
			t = now
		}
		nextSince[c.ID] = t
		out = append(out, Condition{
			ID:          c.ID,
			Severity:    sev,
			Scope:       conditionScope(c),
			Engine:      c.Engine,
			Title:       conditionTitle(c),
			Detail:      c.Detail,
			Why:         c.Why,
			Action:      c.Action,
			Commands:    c.Commands,
			Fix:         c.Fix,
			Installable: c.Installable,
			Since:       t.UTC().Format(time.RFC3339),
		})
	}
	slices.SortStableFunc(out, func(a, b Condition) int {
		if a.Severity != b.Severity {
			if a.Severity == "critical" {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out, nextSince
}

// conditionsChanged reports whether the active condition list changed in a way
// browsers care about.
func conditionsChanged(a, b []Condition) bool {
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Severity != b[i].Severity || a[i].Detail != b[i].Detail || a[i].Installable != b[i].Installable {
			return true
		}
	}
	return false
}

const (
	conditionsPollInterval = 5 * time.Minute
	conditionsPkgWatchTick = 15 * time.Second
	pkgDBPath              = "/var/db/pkg/local.sqlite"
)

func pkgDBModTime() time.Time {
	fi, err := os.Stat(pkgDBPath)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// triggerConditions schedules an immediate background condition check (used
// when an operation hits a known runtime error such as a stale podman socket).
func (s *server) triggerConditions() {
	if s.conds == nil {
		return
	}
	select {
	case s.conds.wake <- struct{}{}:
	default:
	}
}

// refreshConditions runs the doctor checks for the active engines, updates the
// cached condition list, and publishes an SSE event if the list changed.
func (s *server) refreshConditions(ctx context.Context) []Condition {
	if s.conds == nil {
		return nil
	}
	engines := s.engineNames()
	if len(engines) == 0 {
		engines = s.doctorEngines()
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	report := doctor.Run(cctx, doctor.Config{Engines: engines, FjordRoot: s.fjordRoot})
	// Filter out the un-scoped "appjail" check when appjail is not one of the
	// active engines on this host, so a podman-only host does not get a banner
	// about an optional engine.
	hasAppjail := slices.Contains(engines, "appjail")
	filtered := make([]doctor.Result, 0, len(report.Checks))
	for _, ch := range report.Checks {
		if ch.ID == "appjail" && !hasAppjail {
			continue
		}
		filtered = append(filtered, ch)
	}

	now := time.Now()
	s.conds.mu.Lock()
	prev := s.conds.active
	active, nextSince := conditionsFromChecks(filtered, s.conds.since, now)
	s.conds.active = active
	s.conds.since = nextSince
	s.conds.checkedAt = now
	s.conds.pkgMod = pkgDBModTime()
	changed := conditionsChanged(prev, active)
	s.conds.mu.Unlock()

	if changed && s.events != nil {
		if b, err := json.Marshal(map[string]any{
			"type":       "conditions",
			"conditions": active,
		}); err == nil {
			s.events.publish(b)
		}
	}
	return active
}

// runConditionsLoop periodically evaluates doctor checks and watches
// /var/db/pkg/local.sqlite so a `pkg upgrade` triggers a check within seconds.
func (s *server) runConditionsLoop() {
	s.refreshConditions(daemonCtx)
	tick := time.NewTicker(conditionsPkgWatchTick)
	defer tick.Stop()
	lastFull := time.Now()
	for {
		select {
		case <-daemonCtx.Done():
			return
		case <-s.conds.wake:
			s.refreshConditions(daemonCtx)
			lastFull = time.Now()
		case <-tick.C:
			mod := pkgDBModTime()
			s.conds.mu.Lock()
			prevMod := s.conds.pkgMod
			s.conds.mu.Unlock()
			if (!mod.IsZero() && !mod.Equal(prevMod)) || time.Since(lastFull) >= conditionsPollInterval {
				s.refreshConditions(daemonCtx)
				lastFull = time.Now()
			}
		}
	}
}

// handleConditions serves GET /api/conditions from the cache.
func (s *server) handleConditions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var list []Condition
	var checkedAt time.Time
	if s.conds != nil {
		s.conds.mu.Lock()
		list = slices.Clone(s.conds.active)
		checkedAt = s.conds.checkedAt
		s.conds.mu.Unlock()
	}
	if list == nil {
		list = []Condition{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"conditions": list,
		"checkedAt":  rfc3339OrEmpty(checkedAt),
	})
}

// handleConditionsFix runs a condition's installer, then re-runs its check to
// prove the fix worked before reporting "fixed".
func (s *server) handleConditionsFix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID       string `json:"id"`
		TimeZone string `json:"timezone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	cfg := doctor.Config{Engines: s.doctorEngines(), FjordRoot: s.fjordRoot, TimeZone: req.TimeZone}
	var out bytes.Buffer
	installErr := doctor.Install(ctx, cfg, req.ID, &out)
	s.registerNewEngines()

	// Always re-run the check to verify the outcome before claiming it is fixed.
	checkCtx, checkCancel := context.WithTimeout(ctx, 10*time.Second)
	res, ok := doctor.RunOne(checkCtx, cfg, req.ID)
	checkCancel()
	s.refreshConditions(ctx)

	fixed := installErr == nil && ok && res.Status == doctor.OK
	if fixed {
		log.Printf("conditions: fixed and verified %q", req.ID)
	} else if installErr != nil {
		log.Printf("conditions: fix %q failed: %v", req.ID, installErr)
	} else {
		log.Printf("conditions: fix %q ran, but check is still %s (%s)", req.ID, res.Status, res.Detail)
	}

	errMsg := ""
	if installErr != nil {
		errMsg = installErr.Error()
	} else if !fixed {
		errMsg = cmp.Or(strings.TrimSpace(res.Detail), fmt.Sprintf("check %s is still %s after running the fix", req.ID, res.Status))
	}

	w.Header().Set("Content-Type", "application/json")
	if !fixed {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	json.NewEncoder(w).Encode(map[string]any{
		"fixed":  fixed,
		"status": res.Status,
		"detail": res.Detail,
		"output": out.String(),
		"error":  errMsg,
	})
}
