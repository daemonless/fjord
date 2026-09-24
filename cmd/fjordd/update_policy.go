package main

import (
	"encoding/json"
	"net/http"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/registry"
	"github.com/daemonless/fjord/pkg/stack"
	"github.com/daemonless/fjord/pkg/updates"
)

// candidateKey names what an update would install, for the first-seen table:
// the registry digest of a moved tag, or repo:tag for a newer version.
func candidateKey(state, image, latest, newTag string) string {
	switch state {
	case "available":
		return latest
	case "upgrade":
		if newTag != "" {
			return registry.Repo(image) + ":" + newTag
		}
	}
	return ""
}

// markCandidates records every pending update in a check as seen.
func (s *server) markCandidates(st updates.Status) {
	for _, sv := range st.Services {
		s.seen.Mark(candidateKey(sv.State, sv.Image, sv.Latest, sv.NewTag))
	}
}

// policyFor is a service's policy: its own override, else the stack's.
func policyFor(state *stack.State, service string) updates.Policy {
	if state == nil {
		return updates.Off
	}
	raw := state.UpdatePolicy
	if o, ok := state.ServicePolicy[service]; ok {
		raw = o
	}
	p, _ := updates.ParsePolicy(raw)
	return p
}

// verdict is what auto-update would do with one service's pending update.
func (s *server) verdict(st *stack.Stack, service string, class updates.Class, key string) updates.Verdict {
	state, _ := s.manager.LoadState(st.Name)
	return updates.Decide(policyFor(state, service), updates.Candidate{
		Class:      class,
		FirstSeen:  s.seen.Mark(key),
		Dependency: len(composepkg.WithDependents(st.Compose, []string{service})) > 1,
	}, time.Now())
}

// stackPolicy sets a stack's update policy: POST .../policy
// {"policy": "rebuilds", "services": {"database": "rebuilds"}}.
func (s *server) stackPolicy(w http.ResponseWriter, r *http.Request, name string) {
	var req struct {
		Policy   string            `json:"policy"`
		Services map[string]string `json:"services"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	if _, ok := updates.ParsePolicy(req.Policy); !ok {
		http.Error(w, "unknown policy "+req.Policy+": off, notify, rebuilds, patch, minor or all", 400)
		return
	}
	known, _ := composepkg.ServiceImageList(st.Compose)
	for svc, p := range req.Services {
		if _, ok := updates.ParsePolicy(p); !ok {
			http.Error(w, "unknown policy "+p+" for "+svc, 400)
			return
		}
		found := false
		for _, si := range known {
			found = found || si.Service == svc
		}
		if !found {
			http.Error(w, name+" has no service "+svc, 400)
			return
		}
	}
	if err := s.manager.SetUpdatePolicy(name, req.Policy, req.Services); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
