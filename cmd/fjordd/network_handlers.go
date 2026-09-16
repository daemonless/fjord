package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
		nets, err := s.backendForRequest(r).Networks(r.Context())
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
	kinds := s.backendForRequest(r).Capabilities().NetworkKinds
	if kinds == nil {
		kinds = []engine.NetworkKind{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(kinds)
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
