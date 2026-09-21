package main

import (
	"encoding/json"
	"net/http"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/stack"
)

// handleComposeMounts backs the Resources → Storage table. It's stateless: it
// operates on the compose TEXT the client sends (the editor's in-memory value,
// possibly unsaved) and never touches disk, so the compose stays the single
// source of truth. op=list returns the parsed+classified mounts; op=add/remove
// return the transformed compose for the editor to adopt (and Save as usual).
func (s *server) handleComposeMounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Compose  string `json:"compose"`
		Env      string `json:"env"`
		Op       string `json:"op"`      // "list" | "add" | "remove"
		Service  string `json:"service"` // which service to mount into; "" = the first
		Kind     string `json:"kind"`    // add: "bind" | "volume"
		Source   string `json:"source"`
		Dest     string `json:"dest"`
		ReadOnly bool   `json:"readOnly"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	env := (&stack.Stack{Env: req.Env}).EnvMap()

	w.Header().Set("Content-Type", "application/json")
	switch req.Op {
	case "", "list":
		mounts := composepkg.Mounts(req.Compose, env)
		if mounts == nil {
			mounts = []composepkg.Mount{}
		}
		json.NewEncoder(w).Encode(map[string]any{"mounts": mounts})
	case "add":
		var out string
		var err error
		if req.Kind == "volume" {
			out, err = composepkg.AttachVolume(req.Compose, req.Service, req.Source, req.Dest, req.ReadOnly)
		} else {
			out, err = composepkg.AttachBindMount(req.Compose, req.Service, req.Source, req.Dest, req.ReadOnly)
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		json.NewEncoder(w).Encode(transformed(out, req.Env))
	case "remove":
		out, err := composepkg.RemoveMount(req.Compose, req.Service, req.Dest)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		json.NewEncoder(w).Encode(transformed(out, req.Env))
	default:
		http.Error(w, "unknown op: "+req.Op, 400)
	}
}

// transformed is what a mount change hands back: the new compose, and the
// per-service view derived FROM it.
//
// Both, because the caller cannot have one without the other. The services
// list is computed on this side, so a client that changed a mount used to
// re-read the stack to get it -- and re-reading returns what is on DISK,
// which silently discarded the change the client had just been given to hold
// until Save. Adding a bind mount looked like it did nothing at all.
func transformed(compose, env string) map[string]any {
	return map[string]any{
		"compose":  compose,
		"services": composeServiceViews(&stack.Stack{Compose: compose, Env: env}),
	}
}
