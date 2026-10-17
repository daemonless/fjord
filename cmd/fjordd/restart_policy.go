package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

// bootMarker exists from fjordd's first start after a host boot until the
// next boot: /var/run is emptied at boot (cleanvar; tmpfs on Linux), so its
// absence tells a host boot from a fjordd restart.
var bootMarker = "/var/run/fjordd.booted"

// hostBooted reports whether this is fjordd's first start since the host
// booted, and marks it so a later fjordd restart is not one.
func hostBooted() bool {
	if _, err := os.Stat(bootMarker); err == nil {
		return false
	}
	if err := os.WriteFile(bootMarker, nil, 0o644); err != nil {
		log.Printf("start-on-boot: %s: %v", bootMarker, err)
	}
	return true
}

// bootStart is start-on-boot's choice for one stack, from what the user last
// asked for (desired) and its services' `restart:`.
//
//   - Never (every service "no") is not started, boot or fjordd restart.
//   - Always is started at a host boot even when it was stopped, as podman
//     does; a fjordd restart (an upgrade, `service fjordd restart`) leaves a
//     stopped stack stopped.
//   - Everything else -- unless-stopped, on-failure, a stack written before
//     fjord set a policy -- comes up when it was running, as before.
func bootStart(desired string, restarts map[string]string, hostBoot bool) bool {
	always, never := false, len(restarts) > 0
	for _, v := range restarts {
		r, _ := composepkg.ParseRestart(v)
		always = always || r.Policy == composepkg.RestartAlways
		never = never && r.Policy == composepkg.RestartNo
	}
	switch desired {
	case "running":
		return !never
	case "stopped":
		return hostBoot && always
	}
	return false
}

// checkRestart validates a requested policy against what the engine offers;
// retries mean nothing beside a policy other than on-failure.
func checkRestart(r composepkg.Restart, caps engine.Capabilities) (composepkg.Restart, error) {
	if r.Policy != composepkg.RestartOnFailure {
		r.Retries = 0
	}
	if _, ok := composepkg.ParseRestart(r.String()); !ok || !slices.Contains(caps.RestartPolicies, r.Policy) {
		if len(caps.RestartPolicies) == 0 {
			return r, fmt.Errorf("this engine does not take a restart policy from fjord")
		}
		return r, fmt.Errorf("unknown restart policy %q on this engine: %s", r.Policy, strings.Join(caps.RestartPolicies, ", "))
	}
	return r, nil
}

// restartViewOf is a stack's policy for the page: from the compose where the
// engine restarts on exit, from the state where only boot follows it.
func (s *server) restartViewOf(st *stack.Stack) restartView {
	caps := s.backendFor(st).Capabilities()
	v := restartView{Policies: caps.RestartPolicies, OnExit: caps.RestartOnExit}
	if v.Policies == nil {
		v.Policies = []string{}
	}
	switch {
	case caps.RestartOnExit:
		v.StackRestart = composepkg.RestartOf(st.Compose)
	case st.State != nil:
		r, _ := composepkg.ParseRestart(st.State.Restart)
		v.StackRestart = composepkg.StackRestart{Restart: r, Services: map[string]string{}}
	default:
		v.StackRestart.Services = map[string]string{}
	}
	return v
}

// bootRestarts is what start on boot weighs for a stack: each service's
// `restart:`, the state's one answer, or nothing (the old rule: up when it
// was running).
func (s *server) bootRestarts(st *stack.Stack) map[string]string {
	caps := s.backendFor(st).Capabilities()
	switch {
	case caps.RestartOnExit:
		return composepkg.ServiceRestarts(st.Compose)
	case len(caps.RestartPolicies) > 0 && st.State != nil && st.State.Restart != "":
		return map[string]string{"": st.State.Restart}
	}
	return nil
}

// stackRestart sets a stack's restart policy: POST .../restart-policy
// {"policy": "on-failure", "retries": 5}. Where the engine restarts on exit
// it is written to every service's `restart:` (a one-shot keeps its "no") and
// recorded for Apply like a Save: the containers take it when next created.
// Elsewhere it is kept in the stack's state, for start on boot.
func (s *server) stackRestart(w http.ResponseWriter, r *http.Request, name string) {
	var req composepkg.Restart
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	unlock, ok := lockStack(name)
	if !ok {
		http.Error(w, anotherOperation(name), http.StatusConflict)
		return
	}
	defer unlock()
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	caps := s.backendFor(st).Capabilities()
	if req, err = checkRestart(req, caps); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if !caps.RestartOnExit {
		if err := s.manager.UpdateState(name, func(state *stack.State) *stack.State {
			if state == nil {
				state = &stack.State{}
			}
			state.Restart = req.String()
			return state
		}); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		log.Printf("%s: restart policy %s (boot only)", name, req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "saved"})
		return
	}
	compose, err := composepkg.SetRestart(st.Compose, req)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	old := st.Compose
	st.Compose = compose
	if err := s.manager.Save(st); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	env := st.EnvMap()
	s.recordPending(name, composepkg.ChangedServices(old, env, compose, env))
	log.Printf("%s: restart policy %s", name, req)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "saved", "composeHash": composeHash(st)})
}

// defaultRestartFor is the policy new installs on an engine take: the saved
// default for it, or unless-stopped when there is none or the engine does not
// offer it.
func defaultRestartFor(st savedSettings, eng string, caps engine.Capabilities) composepkg.Restart {
	if r, ok := composepkg.ParseRestart(st.DefaultRestartFor[eng]); ok && slices.Contains(caps.RestartPolicies, r.Policy) {
		return r
	}
	return composepkg.Restart{Policy: composepkg.RestartUnlessStopped}
}

// handleRestartSettings reads (GET) the restart policy new installs take on
// each engine that offers one, or sets (POST {"engine": "podman", "policy":
// "on-failure", "retries": 5}) one engine's.
func (s *server) handleRestartSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var req struct {
			Engine string `json:"engine"`
			composepkg.Restart
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", 400)
			return
		}
		be, ok := s.backend(req.Engine)
		if !ok {
			http.Error(w, "no engine "+req.Engine, 400)
			return
		}
		policy, err := checkRestart(req.Restart, be.Capabilities())
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) {
			if st.DefaultRestartFor == nil {
				st.DefaultRestartFor = map[string]string{}
			}
			st.DefaultRestartFor[req.Engine] = policy.String()
		}); err != nil {
			http.Error(w, "persist: "+err.Error(), 500)
			return
		}
	default:
		http.Error(w, "method not allowed", 405)
		return
	}
	st := loadSettings(s.fjordRoot)
	out := map[string]composepkg.Restart{}
	for _, d := range engineDescriptors {
		if be, ok := s.backend(d.Name); ok && len(be.Capabilities().RestartPolicies) > 0 {
			out[d.Name] = defaultRestartFor(st, d.Name, be.Capabilities())
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
