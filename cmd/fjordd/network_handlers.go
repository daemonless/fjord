package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
)

// handleNetworks lists (GET) or creates (POST) the networks that give a stack
// its own IP. Creation takes the runtime-neutral engine.NetworkSpec; the
// backend translates it (a CNI conflist on FreeBSD podman, a virtualnet on
// appjail).
func (s *server) handleNetworks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		var nets []engine.Network
		var err error
		if r.URL.Query().Get("engine") != "" || r.URL.Query().Get("stack") != "" {
			// Scoped: what THIS engine can attach to, for an install or a
			// stack's own view.
			nets, err = s.backendForRequest(r).Networks(r.Context())
		} else {
			nets, err = s.allNetworks(r.Context())
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if nets == nil {
			nets = []engine.Network{} // encode [] not null so the UI can .length it
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(nets)
	case http.MethodPost:
		var spec engine.NetworkSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil || spec.Name == "" {
			http.Error(w, "name required", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		n, err := s.backendForRequest(r).CreateNetwork(ctx, spec)
		if err != nil {
			// The backend validates the spec (name, subnet, gateway-in-subnet);
			// those are the user's input, so they are 400s, not gateway errors.
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(n)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// handleNetworkKinds reports the kinds of network the selected engine can
// create, and which spec fields each uses, so the UI builds its form from the
// engine's own declaration instead of branching on an engine name. An empty
// list means this engine creates no networks (e.g. podman on Linux).
func (s *server) handleNetworkKinds(w http.ResponseWriter, r *http.Request) {
	if e := r.URL.Query().Get("engine"); e != "" {
		caps := s.backendForRequest(r).Capabilities()
		kinds := caps.NetworkKinds
		if kinds == nil {
			kinds = []engine.NetworkKind{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"kinds": kinds, "note": caps.NetworkNote, "canRemove": caps.NetworkRemove,
		})
		return
	}
	// Unscoped: every kind any engine can make, tagged with which one makes
	// it, so the form offers a kind rather than making the user pick an
	// engine and then discover what that engine happens to support.
	kinds := []engine.NetworkKind{}
	notes := []string{}
	for _, name := range s.engineNames() {
		be, ok := s.backend(name)
		if !ok {
			continue
		}
		caps := be.Capabilities()
		for _, k := range caps.NetworkKinds {
			k.Engine = name
			kinds = append(kinds, k)
		}
		if caps.NetworkNote != "" && len(caps.NetworkKinds) == 0 {
			notes = append(notes, caps.NetworkNote)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"kinds": kinds, "note": strings.Join(notes, " "), "canRemove": true,
	})
}

// handleNetworkParents lists host interfaces a "lan" network can attach to.
// Empty for engines whose networks take no parent.
func (s *server) handleNetworkParents(w http.ResponseWriter, r *http.Request) {
	parents, err := s.backendForRequest(r).NetworkParents(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if parents == nil {
		parents = []engine.NetworkParent{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(parents)
}

// handleNetworkDelete removes a network: DELETE /api/networks/<name>[?force=true].
func (s *server) handleNetworkDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", 405)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/networks/")
	if name == "" || name == "parents" || name == "kinds" {
		http.Error(w, "name required", 400)
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if err := s.backendForRequest(r).RemoveNetwork(r.Context(), name, force); err != nil {
		// Attached containers are a 409 the UI can act on (offer force).
		if errors.Is(err, engine.ErrInUse) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"deleted"}`))
}

// networkUnusable reports why an engine cannot place a stack on the requested
// network, or "" when it can. Returning a reason is the point: writing a
// network into a stack the engine then ignores produces a running stack on a
// silently different address, which looks like a working install until
// something tries to reach the address that was asked for.
func (s *server) networkUnusable(ctx context.Context, engineName, network string) string {
	be, ok := s.backend(engineName)
	if !ok {
		return ""
	}
	nets, err := be.Networks(ctx)
	if err != nil {
		return "" // can't verify; let the attach itself report a problem
	}
	for _, n := range nets {
		if n.Name == network {
			return ""
		}
	}
	var names []string
	for _, n := range nets {
		names = append(names, n.Name)
	}
	if len(names) == 0 {
		return fmt.Sprintf("no network named %q is available to this engine, and it has none to offer", network)
	}
	return fmt.Sprintf("no network named %q is available to this engine; it offers %s", network, strings.Join(names, ", "))
}

// allNetworks merges every engine's view into one list, recording which
// engines can attach to each. The same LAN bridge is reported by both
// runtimes; that is one network, not two.
func (s *server) allNetworks(ctx context.Context) ([]engine.Network, error) {
	byName := map[string]*engine.Network{}
	var order []string
	for _, name := range s.engineNames() {
		be, ok := s.backend(name)
		if !ok {
			continue
		}
		nets, err := be.Networks(ctx)
		if err != nil {
			continue // one engine being unreachable must not empty the page
		}
		for _, n := range nets {
			cur, seen := byName[n.Name]
			if !seen {
				cp := n
				cp.Engines = []string{name}
				byName[n.Name] = &cp
				order = append(order, n.Name)
				continue
			}
			cur.Engines = append(cur.Engines, name)
			// Engines describe the same network differently: podman knows the
			// conflist's subnet, appjail knows which jails are on the bridge.
			// Keep whatever is populated.
			if cur.Subnet == "" {
				cur.Subnet, cur.Gateway = n.Subnet, n.Gateway
			}
			if cur.Problem == "" {
				cur.Problem = n.Problem
			}
			cur.UsedBy = append(cur.UsedBy, n.UsedBy...)
		}
	}
	out := make([]engine.Network, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// engineNames lists the registered engines, default first so its description
// of a shared network wins.
func (s *server) engineNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := []string{}
	if _, ok := s.backends[s.defEngine]; ok {
		names = append(names, s.defEngine)
	}
	for n := range s.backends {
		if n != s.defEngine {
			names = append(names, n)
		}
	}
	return names
}
