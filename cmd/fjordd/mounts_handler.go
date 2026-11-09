package main

import (
	"encoding/json"
	"net/http"
	"runtime"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

// handleComposeMounts backs the Resources → Storage table. It's stateless: it
// operates on the compose TEXT the client sends (the editor's in-memory value,
// possibly unsaved) and never touches disk, so the compose stays the single
// source of truth. op=list returns the parsed+classified mounts; op=add,
// remove and options return the transformed compose for the editor to adopt
// (and Save as usual).
func (s *server) handleComposeMounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Compose string `json:"compose"`
		Env     string `json:"env"`
		// Director is the appjail-director.yml of a director stack: the file
		// its mounts live in, which replaces Compose as what is edited. Name
		// is the stack's, for the jail names in the view handed back.
		Director string   `json:"director"`
		Name     string   `json:"name"`
		Op       string   `json:"op"`      // "list" | "add" | "remove" | "options"
		Service  string   `json:"service"` // which service to mount into; "" = the first
		Kind     string   `json:"kind"`    // add: "bind" | "volume"
		Source   string   `json:"source"`
		Dest     string   `json:"dest"`
		ReadOnly bool     `json:"readOnly"` // the same as "ro" in Options
		Options  []string `json:"options"`  // add, options: "ro", "z", ...
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	env := (&stack.Stack{Env: req.Env}).EnvMap()
	opts := composepkg.WithReadOnly(req.Options, req.ReadOnly)
	if strings.TrimSpace(req.Director) != "" {
		s.handleDirectorMounts(w, req.Director, req.Name, req.Env, req.Op, req.Service, req.Kind, req.Source, req.Dest, opts)
		return
	}
	if req.Op == "add" || req.Op == "options" {
		// Refused here, at the edit: podman only says "invalid option type"
		// when the stack starts.
		if err := composepkg.CheckMountOptions(opts, runtime.GOOS); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}

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
			out, err = composepkg.AttachVolume(req.Compose, req.Service, req.Source, req.Dest, opts)
		} else {
			out, err = composepkg.AttachBindMount(req.Compose, req.Service, req.Source, req.Dest, opts)
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		json.NewEncoder(w).Encode(s.transformed(out, req.Env))
	case "remove":
		out, err := composepkg.RemoveMount(req.Compose, req.Service, req.Dest)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		json.NewEncoder(w).Encode(s.transformed(out, req.Env))
	case "options":
		out, err := composepkg.SetMountOptions(req.Compose, req.Service, req.Dest, opts)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		json.NewEncoder(w).Encode(s.transformed(out, req.Env))
	default:
		http.Error(w, "unknown op: "+req.Op, 400)
	}
}

// handleDirectorMounts is handleComposeMounts for a director stack: the same
// ops on appjail-director.yml, handing back that file and the services as
// read from it. A named volume is podman's; an appjail mount is a host path.
func (s *server) handleDirectorMounts(w http.ResponseWriter, director, name, env, op, service, kind, source, dest string, opts []string) {
	st := &stack.Stack{Name: name, Director: director, Env: env}
	if name != "" && s.manager != nil {
		if saved, err := s.manager.Get(name); err == nil {
			st.State = saved.State
		}
	}
	var out string
	var err error
	switch op {
	case "", "list":
		out = director
	case "add":
		if kind == "volume" {
			http.Error(w, "an AppJail stack mounts host folders; named volumes are podman's", 400)
			return
		}
		out, err = s.editDirectorMount(st, op, director, service, source, dest, opts)
	case "remove", "options":
		out, err = s.editDirectorMount(st, op, director, service, source, dest, opts)
	default:
		http.Error(w, "unknown op: "+op, 400)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	views := directorServiceViews(&stack.Stack{Name: name, Director: out, Env: env})
	if op == "" || op == "list" {
		var mounts []composepkg.Mount
		for _, v := range views {
			for _, m := range v.Volumes {
				mounts = append(mounts, composepkg.Mount{Source: m.Source, Dest: m.Dest, ReadOnly: m.ReadOnly, Options: m.Options, Kind: m.Kind, Service: v.Name})
			}
		}
		if mounts == nil {
			mounts = []composepkg.Mount{}
		}
		json.NewEncoder(w).Encode(map[string]any{"mounts": mounts})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"director": out, "services": views})
}

func (s *server) editDirectorMount(st *stack.Stack, op, director, service, source, dest string, opts []string) (string, error) {
	if ed, ok := s.backendFor(st).(engine.SpecMountEditor); ok {
		switch op {
		case "add":
			return ed.AttachSpecBindMount(director, service, source, dest, opts)
		case "remove":
			return ed.RemoveSpecMount(director, service, dest)
		case "options":
			return ed.SetSpecMountOptions(director, service, dest, opts)
		}
	}
	switch op {
	case "add":
		return directorAttachBindMount(director, service, source, dest, opts)
	case "remove":
		return directorRemoveMount(director, service, dest)
	case "options":
		return directorSetMountOptions(director, service, dest, opts)
	default:
		return "", nil
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
func (s *server) transformed(compose, env string) map[string]any {
	views := composeServiceViews(&stack.Stack{Compose: compose, Env: env})
	s.markSELinux(views)
	return map[string]any{"compose": compose, "services": views}
}

// mountOption is an option the mount form offers by name; the rest are typed.
type mountOption struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Help  string `json:"help"`
}

// handleMountOptions lists the options the mount form offers on this host:
// read-only everywhere, the SELinux labels where SELinux is enforcing (on
// FreeBSD podman takes z and Z and does nothing with them), and U.
func (s *server) handleMountOptions(w http.ResponseWriter, r *http.Request) {
	out := []mountOption{{Name: "ro", Label: "Read-only", Help: "The container can read the folder but not change it."}}
	if selinuxEnforcing() {
		out = append(out,
			mountOption{Name: "z", Label: "SELinux: shared", Help: "Relabel the folder so this and other containers can use it."},
			mountOption{Name: "Z", Label: "SELinux: private", Help: "Relabel the folder for this container alone; others are refused it."})
	}
	out = append(out, mountOption{Name: "U", Label: "Owned by the app",
		Help: "Change the folder's owner to the container's user at start. Changes the files on the host, every one of them."})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"offered": out})
}

// handleComposePassEnv backs the .env tab's "Pass to the app": the compose
// the client holds, with NAME=${NAME} added to a service's environment for
// each name. An edit like a mount change, held until Save.
func (s *server) handleComposePassEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Compose string   `json:"compose"`
		Env     string   `json:"env"`
		Service string   `json:"service"` // "" = the first
		Names   []string `json:"names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	if req.Service == "" {
		req.Service = composepkg.FirstService
	}
	out, err := composepkg.PassEnv(req.Compose, req.Service, req.Names)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.transformed(out, req.Env))
}
