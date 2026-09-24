package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
	"github.com/daemonless/fjord/pkg/updates"
)

// updateServices is a stack's services as the update check needs them: each
// image with its ${VAR}s expanded against the .env -- refs like
// "immich-server:${IMMICH_TAG:-latest}" are unresolvable at the registry
// otherwise -- and the digest its container is actually running.
//
// An engine that cannot report running images leaves Running empty, and the
// check falls back to comparing the local tag.
func (s *server) updateServices(ctx context.Context, st *stack.Stack) []updates.Service {
	list, _ := composepkg.ServiceImageList(st.Compose)
	env := st.EnvMap()
	running := map[string]engine.RunningImage{}
	if ris, err := s.backendFor(st).RunningImages(ctx, st); err == nil {
		for _, ri := range ris {
			running[ri.Service] = ri
		}
	}
	out := make([]updates.Service, 0, len(list))
	seen := map[string]bool{}
	for _, si := range list {
		seen[si.Service] = true
		out = append(out, updates.Service{
			Name:    si.Service,
			Image:   composepkg.ExpandEnv(si.Image, env),
			Running: running[si.Service].Digest,
			Known:   running[si.Service].Digests,
		})
	}
	// A service the compose names no image for, but whose engine knows what
	// it runs: an appjail director stack names makejails, not images, so the
	// ref the jail was built from is the only image there is to check.
	for _, ri := range running {
		if !seen[ri.Service] && ri.Ref != "" {
			out = append(out, updates.Service{Name: ri.Service, Image: ri.Ref, Running: ri.Digest, Known: ri.Digests})
		}
	}
	extra := out[len(list):] // map order is random; keep the panel's rows stable
	sort.Slice(extra, func(i, j int) bool { return extra[i].Name < extra[j].Name })
	return out
}

// fleetUpdates is a cached fleet-wide update check. Registry lookups are slow
// and rate-limited, so results are cached and refreshed in the background
// (single-flight); readers always get the current cache immediately.
type fleetUpdates struct {
	mu         sync.Mutex
	results    map[string]updates.Status
	checkedAt  time.Time
	refreshing bool
	// touched is when each stack's entry was last replaced or dropped
	// outside a refresh. A refresh checks stacks one by one for minutes; an
	// update that finished meanwhile had its "behind" written straight back
	// by the swap, and the badge stayed for the whole TTL.
	touched map[string]time.Time
}

const fleetCacheTTL = 30 * time.Minute

// fleetResponse is the /api/updates wire format.
type fleetResponse struct {
	Stacks     map[string]updates.Status `json:"stacks"`
	CheckedAt  string                    `json:"checkedAt,omitempty"`
	Refreshing bool                      `json:"refreshing"`
}

// handleUpdates serves the cached fleet-wide update state. A stale cache (or
// ?refresh=1) kicks a background refresh; the response never blocks on the
// registry.
func (s *server) handleUpdates(w http.ResponseWriter, r *http.Request) {
	s.fleet.mu.Lock()
	stale := time.Since(s.fleet.checkedAt) > fleetCacheTTL
	if (stale || r.URL.Query().Get("refresh") == "1") && !s.fleet.refreshing {
		s.fleet.refreshing = true
		go s.refreshFleet()
	}
	resp := fleetResponse{Stacks: s.fleet.current(s.manager), Refreshing: s.fleet.refreshing}
	if !s.fleet.checkedAt.IsZero() {
		resp.CheckedAt = s.fleet.checkedAt.UTC().Format(time.RFC3339)
	}
	s.fleet.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// refreshFleet re-checks every stack against the registry, then swaps the
// cache in one shot. Serial on purpose: gentle on the registry.
func (s *server) refreshFleet() {
	started := time.Now()
	results := map[string]updates.Status{}
	if stacks, err := s.manager.List(); err == nil {
		for _, st := range stacks {
			full, err := s.manager.Get(st.Name)
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			results[st.Name] = updates.Check(ctx, s.backendFor(full), s.updateServices(ctx, full), s.schemeFor)
			s.markCandidates(results[st.Name])
			cancel()
		}
	}
	s.fleet.mu.Lock()
	s.fleet.swap(results, started)
	s.fleet.checkedAt = time.Now()
	s.fleet.refreshing = false
	s.fleet.mu.Unlock()
}

// current returns the cached results restricted to stacks that still exist,
// so a deleted stack's "update available" doesn't linger until the next
// refresh. Caller holds mu.
func (f *fleetUpdates) current(m *stack.Manager) map[string]updates.Status {
	stacks, err := m.List()
	if err != nil {
		return f.results
	}
	out := make(map[string]updates.Status, len(f.results))
	for _, st := range stacks {
		if r, ok := f.results[st.Name]; ok {
			out[st.Name] = r
		}
	}
	return out
}

// forget drops a stack from the cache (on delete, and after up/update).
func (f *fleetUpdates) forget(name string) {
	f.mu.Lock()
	delete(f.results, name)
	f.touch(name)
	f.mu.Unlock()
}

// put stores a stack's fresh check -- the stack page's own, which is newer
// than whatever the last fleet refresh saw.
func (f *fleetUpdates) put(name string, st updates.Status) {
	f.mu.Lock()
	if f.results == nil {
		f.results = map[string]updates.Status{}
	}
	f.results[name] = st
	f.touch(name)
	f.mu.Unlock()
}

// swap installs a refresh's results, except for stacks changed since it
// started: those keep what they have now (or stay dropped). Caller holds mu.
func (f *fleetUpdates) swap(results map[string]updates.Status, started time.Time) {
	for name, at := range f.touched {
		if !at.After(started) {
			continue
		}
		if cur, ok := f.results[name]; ok {
			results[name] = cur
		} else {
			delete(results, name)
		}
	}
	f.touched = nil
	f.results = results
}

// touch records an out-of-refresh change. Caller holds mu.
func (f *fleetUpdates) touch(name string) {
	if f.touched == nil {
		f.touched = map[string]time.Time{}
	}
	f.touched[name] = time.Now()
}
