package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// handleDefaultNetwork reads and sets the network new installs start on.
//
// Host ports is the right default for one machine with one app on it, and the
// wrong one the moment an operator has decided every stack gets its own
// address: they then pick the same network in every install, and forgetting
// once is a stack that silently binds host ports instead.
func (s *server) handleDefaultNetwork(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"network": loadSettings(s.fjordRoot).DefaultNetwork})
	case http.MethodPost:
		var req struct {
			Network string `json:"network"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", 400)
			return
		}
		// "" clears it. Anything else has to exist now, or every install
		// afterwards fails on a network that was renamed or removed.
		if req.Network != "" {
			nets, err := s.allNetworks(r.Context())
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			found := false
			for _, n := range nets {
				if n.Name == req.Network {
					found = true
					break
				}
			}
			if !found {
				http.Error(w, "no network named "+req.Network+" on this host", 400)
				return
			}
		}
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.DefaultNetwork = req.Network }); err != nil {
			http.Error(w, "persist: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"network": req.Network})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// handleNetworkSetup re-renders one parent-setup snippet with the choices the
// host cannot make for itself: which NIC is cabled to the segment the
// containers belong on, and which VLAN the switch tags on that port.
func (s *server) handleNetworkSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	b, ok := s.backendForRequest(r).(engine.NetworkSetupper)
	if !ok {
		http.Error(w, "this engine has no parent setup to vary", 404)
		return
	}
	q := r.URL.Query()
	ps, err := b.ParentSetup(r.Context(), q.Get("kind"), q.Get("nic"), q.Get("vlan"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ps)
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
	at := map[string]int{} // kind id -> its slot in kinds
	notes := []string{}
	// engineNames puts the default engine first, so the first declaration of
	// a kind wins: the one that will make it unless the user says otherwise.
	// The rest only add themselves to Engines -- two engines offering the
	// same kind is one row on the form, not two.
	for _, name := range s.engineNames() {
		be, ok := s.backend(name)
		if !ok {
			continue
		}
		caps := be.Capabilities()
		for _, k := range caps.NetworkKinds {
			if i, seen := at[k.ID]; seen {
				kinds[i].Engines = append(kinds[i].Engines, name)
				continue
			}
			k.Engine = name
			k.Engines = []string{name}
			at[k.ID] = len(kinds)
			kinds = append(kinds, k)
		}
		// Whenever there is one, not only when the engine offers nothing at
		// all. Since a private network works everywhere, "offers nothing"
		// stopped happening -- and the note explaining why the LAN option is
		// missing went with it, leaving the feature looking absent rather
		// than unavailable.
		if caps.NetworkNote != "" {
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
	// A default pointing at a network that no longer exists is a value nothing
	// can act on: the wizard skips it, the Networks page shows no Default mark,
	// and the setting sits there being wrong. Deleting the network is the
	// moment to clear it.
	if loadSettings(s.fjordRoot).DefaultNetwork == name {
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.DefaultNetwork = "" }); err != nil {
			log.Printf("clearing default network after deleting %s: %v", name, err)
		}
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
	offers := engineName + " has no networks it can use"
	var names []string
	for _, n := range nets {
		names = append(names, n.Name)
	}
	if len(names) > 0 {
		offers = engineName + " can use: " + strings.Join(names, ", ")
	}
	// A name the OTHER engine owns is the common way to land here, and "not
	// available" reads like a bug when the network is listed on the Networks
	// page. Say whose it is and why that stops this engine using it.
	if all, err := s.allNetworks(ctx); err == nil {
		for _, n := range all {
			if n.Name != network || len(n.Engines) == 0 {
				continue
			}
			return fmt.Sprintf("%q belongs to %s, which hands out its addresses itself: %s attaching as well would put two allocators on one segment. A network both engines can share is defined on the host instead -- %s",
				network, strings.Join(n.Engines, " and "), engineName, offers)
		}
	}
	return fmt.Sprintf("no network named %q on this host -- %s", network, offers)
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
