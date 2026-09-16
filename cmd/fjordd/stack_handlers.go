package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/registry"
	"github.com/daemonless/fjord/pkg/stack"
	"github.com/daemonless/fjord/pkg/updates"
)

// saveRequest is the /api/stacks/<n>/save payload: the editable stack fields
// plus an optional macvlan attachment applied to the compose before writing.
// Network/IP are only sent when a stack is first created/attached, not on
// every edit.
type saveRequest struct {
	Compose string `json:"compose"`
	Env     string `json:"env"`
	// Director/Makejail are the appjail-director.yml and Makejail: accepted on
	// create when Engine is appjail (a native appjail stack, no compose), and on
	// edit only for a stack that already runs via director (a compose stack
	// can't sprout one).
	Director string `json:"director,omitempty"`
	Makejail string `json:"makejail,omitempty"`
	// Engine binds a NEW stack to a runtime (podman|appjail); ignored on edit.
	Engine string `json:"engine,omitempty"`
	// DisplayName labels a NEW stack (renaming an existing one is /rename).
	DisplayName string `json:"displayName,omitempty"`
	Network     string `json:"network,omitempty"` // attach the stack to this macvlan network
	IP          string `json:"ip,omitempty"`      // optional predictable IP within it
	// One-shot volume attachment: mount the named volume at VolumePath.
	Volume     string `json:"volume,omitempty"`
	VolumePath string `json:"volumePath,omitempty"`
	VolumeRO   bool   `json:"volumeRO,omitempty"`
}

// handleStacksList lists stacks with their compose/.env and live container
// status. Carrying the (small) compose text here lets the dashboard derive
// tags and links for every tile from one request instead of one per stack.
func (s *server) handleStacksList(w http.ResponseWriter, r *http.Request) {
	stacks, err := s.manager.List()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	enriched := make([]stackWithStatus, 0, len(stacks))
	for _, st := range stacks {
		if full, err := s.manager.Get(st.Name); err == nil {
			st = full
		}
		status, err := s.backendFor(st).Status(r.Context(), st)
		if err != nil {
			status = engine.StackStatus{State: "unknown"}
		}
		enriched = append(enriched, stackWithStatus{Stack: st, Status: status})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(enriched)
}

// handleStackRoutes dispatches /api/stacks/<name>[/<action>].
func (s *server) handleStackRoutes(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/stacks/"), "/")
	// The name becomes a host path component; the mux only canonicalises
	// literal dots, so an escaped %2E%2E still arrives here as "..".
	if !stack.ValidName(parts[0]) {
		http.Error(w, "invalid stack name: use letters, digits, '_' or '-'", 400)
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			s.stackDetail(w, r, parts[0])
			return
		case http.MethodDelete:
			s.stackDelete(w, parts[0])
			return
		}
	}
	if r.Method == http.MethodGet && len(parts) == 2 {
		switch parts[1] {
		case "update-check":
			s.stackUpdateCheck(w, parts[0])
			return
		case "icon":
			s.stackIcon(w, r, parts[0])
			return
		case "exec":
			s.stackExec(w, r, parts[0])
			return
		case "logs":
			s.stackLogs(w, r, parts[0])
			return
		}
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	name, action := parts[0], parts[1]
	switch action {
	case "save":
		s.stackSave(w, r, name)
	case "set-tag":
		s.stackSetTag(w, r, name)
	case "group":
		s.stackGroup(w, r, name)
	case "rename":
		s.stackRename(w, r, name)
	default:
		s.stackLifecycle(w, r, name, action)
	}
}

// stackDetail returns the full stack (compose, env, state) plus live container
// status. The list endpoint omits compose content, so the editor fetches it
// here; status is included so a deep-link/refresh doesn't render "unknown".
func (s *server) stackDetail(w http.ResponseWriter, r *http.Request, name string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	status, err := s.backendFor(st).Status(r.Context(), st)
	if err != nil {
		status = engine.StackStatus{State: "unknown"}
	}
	w.Header().Set("Content-Type", "application/json")
	net, ip := composepkg.AttachedNetwork(st.Compose)
	json.NewEncoder(w).Encode(stackWithStatus{Stack: st, Status: status, Network: net, NetworkIP: ip})
}

// stackDelete stops the stack's containers, then removes its stack dir.
// Bind-mounted container data is untouched. If containers survive the
// teardown (Down streams its errors rather than returning them), the delete
// is refused: removing the dir would orphan running containers that fjord
// could no longer see or stop.
func (s *server) stackDelete(w http.ResponseWriter, name string) {
	if st, err := s.manager.Get(name); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		be := s.backendFor(st)
		if stream, err := be.Down(ctx, st); err == nil {
			io.Copy(io.Discard, stream) // block until teardown finishes
			stream.Close()
		}
		if status, err := be.Status(ctx, st); err == nil {
			var alive []string
			for _, c := range status.Containers {
				if c.State != "stopped" && c.State != "" {
					alive = append(alive, c.Name+" ("+c.State+")")
				}
			}
			if len(alive) > 0 {
				cancel()
				http.Error(w, "not deleted: containers are still up after the stop attempt -- "+strings.Join(alive, ", ")+". Check Output/Logs, stop them, then delete again.", http.StatusConflict)
				return
			}
		}
		cancel()
	}
	if err := s.manager.Delete(name); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Stack not found", 404)
		} else {
			http.Error(w, err.Error(), 500)
		}
		return
	}
	log.Printf("delete %s", name)
	s.fleet.forget(name) // no lingering "update available" for a gone stack
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"deleted"}`))
}

// stackRename sets a stack's display name -- an instant metadata edit. The
// stack's identity (dir, compose project, containers, volumes) is the id and
// never changes, so nothing is stopped or recreated.
func (s *server) stackRename(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		NewName string `json:"newName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	if !stack.ValidDisplayName(req.NewName) {
		http.Error(w, "invalid name: letters, digits, spaces, '.', '_' and '-' only (max 64)", 400)
		return
	}
	if _, err := s.manager.Get(id); err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	if err := s.manager.SetDisplayName(id, req.NewName); err != nil {
		http.Error(w, "rename: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "renamed", "displayName": req.NewName})
}

// stackUpdateCheck reports whether the stack's images are behind the registry.
func (s *server) stackUpdateCheck(w http.ResponseWriter, name string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	images := resolvedImages(st)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	status, err := updates.Check(ctx, s.backendFor(st), images, s.schemeFor)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// stackLogs streams container logs: GET .../logs?tail=200&follow=1. Uses the
// request context so a client disconnect kills the log process (important for
// follow, which otherwise never exits).
func (s *server) stackLogs(w http.ResponseWriter, r *http.Request, name string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	tail := 200
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, e := strconv.Atoi(t); e == nil {
			tail = n
		}
	}
	stream, err := s.backendFor(st).Logs(r.Context(), st, tail, r.URL.Query().Get("follow") == "1", r.URL.Query()["container"])
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	streamOutput(w, stream)
}

// stackSave persists edited compose/.env, optionally attaching a macvlan
// network so the stack gets its own IP.
func (s *server) stackSave(w http.ResponseWriter, r *http.Request, name string) {
	var payload saveRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON payload", 400)
		return
	}
	// A top-level `name:` would make podman-compose label containers with it
	// instead of the stack id, hiding them from status/logs/delete -- the
	// same strip the catalog install path applies.
	composeYAML := composepkg.DropTopLevelKey(payload.Compose, "name")
	if payload.Network != "" {
		eng := payload.Engine
		if eng == "" {
			if existing, err := s.manager.Get(name); err == nil {
				eng = existing.EngineName()
			}
		}
		if msg := s.networkUnusable(r.Context(), eng, payload.Network); msg != "" {
			http.Error(w, msg, 400)
			return
		}
		injected, err := composepkg.InjectNetwork(composeYAML, payload.Network, payload.IP)
		if err != nil {
			http.Error(w, "network attach: "+err.Error(), 400)
			return
		}
		composeYAML = injected
	}
	if payload.Volume != "" {
		attached, err := composepkg.AttachVolume(composeYAML, payload.Volume, payload.VolumePath, payload.VolumeRO)
		if err != nil {
			http.Error(w, "volume attach: "+err.Error(), 400)
			return
		}
		composeYAML = attached
	}
	// If it's a new stack it might not exist on disk yet; use the payload.
	st := &stack.Stack{Name: name, Compose: composeYAML, Env: payload.Env}
	created := false
	if existing, err := s.manager.Get(name); err == nil {
		existing.Compose = composeYAML
		existing.Env = payload.Env
		if existing.Director != "" {
			if payload.Director != "" {
				existing.Director = payload.Director
			}
			if payload.Makejail != "" {
				existing.Makejail = payload.Makejail
			}
		}
		st = existing
	} else {
		created = true
		// A new native appjail stack is director + Makejail, no compose.
		if payload.Engine == "appjail" && payload.Director != "" {
			if _, ok := s.backend("appjail"); !ok {
				http.Error(w, "the appjail engine is not available on this host", 400)
				return
			}
			if strings.TrimSpace(payload.Makejail) == "" {
				http.Error(w, "an appjail stack needs a Makejail (image source for its jails)", 400)
				return
			}
			st.Director, st.Makejail = payload.Director, payload.Makejail
		}
	}
	if err := s.manager.Save(st); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if created {
		eng := payload.Engine
		if eng == "" {
			eng = s.defaultEngine()
		}
		log.Printf("create %s (%s, director=%v)", name, eng, st.Director != "")
	} else {
		log.Printf("save %s", name)
	}
	// Record installed/updated timestamps; leaves desired_state alone.
	if err := s.manager.EnsureState(name); err != nil {
		log.Printf("save %s: persist state: %v", name, err)
	}
	// Bind a new stack to its chosen engine (only if that engine is registered;
	// unset means podman). Existing stacks keep theirs -- switching runtimes is
	// a recreate, not an edit.
	if created && payload.Engine != "" {
		if _, ok := s.backend(payload.Engine); !ok {
			http.Error(w, "unknown engine "+payload.Engine, 400)
			return
		}
		if err := s.manager.SetEngine(name, payload.Engine); err != nil {
			log.Printf("save %s: set engine: %v", name, err)
		}
	}
	if created && strings.TrimSpace(payload.DisplayName) != "" {
		if err := s.manager.SetDisplayName(name, payload.DisplayName); err != nil {
			log.Printf("save %s: set display name: %v", name, err)
		}
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"saved"}`))
}

// stackSetTag rewrites the stack's image ref -- change train/version and/or
// pin to an exact digest. pin=true freezes each service image to the digest
// its tag resolves to now ("repo:tag" -> "repo:tag@sha256:..."), so a later
// tag move can't change what's deployed. SetImageTag drops any existing
// @digest first, so pin=false (or omitted) unpins. The UI redeploys afterward
// only when the version actually changed.
func (s *server) stackSetTag(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		Tag string `json:"tag"`
		Pin bool   `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Tag == "" {
		http.Error(w, "tag required", 400)
		return
	}
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	// SetImageTag rewrites EVERY service image -- correct for single-image
	// stacks, destructive for multi-image ones (it would retag the db/redis
	// to an app version). Those edit their compose directly.
	if images, _ := composepkg.ServiceImages(st.Compose); len(images) > 1 {
		http.Error(w, "multi-service stack: set image tags in the compose editor", 400)
		return
	}
	newCompose, err := composepkg.SetImageTag(st.Compose, body.Tag)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if body.Pin {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		newCompose, err = composepkg.PinImageDigests(newCompose, func(ref string) (string, error) {
			return registry.Digest(ctx, ref)
		})
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
	}
	st.Compose = newCompose
	if err := s.manager.Save(st); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// stackGroup assigns the stack to a sidebar group (empty = ungrouped).
func (s *server) stackGroup(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		Group string `json:"group"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	if err := s.manager.SetGroup(name, strings.TrimSpace(body.Group)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// handleReorder persists the sidebar's drag-and-drop layout: the client sends
// every stack in its new top-to-bottom order with its (possibly changed) group,
// and the manager stamps order + group in one pass.
func (s *server) handleReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Items []stack.OrderItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	if err := s.manager.Reorder(body.Items); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// stackLifecycle runs up/down/update/restart, streaming the backend's output.
func (s *server) stackLifecycle(w http.ResponseWriter, r *http.Request, name, action string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}

	// Generous timeout: update pulls images, which can be slow.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// One line per operator action: without it an incident has to be
	// reconstructed from state.json mtimes and podman events.
	log.Printf("%s %s (%s)", action, name, st.EngineName())

	// Catch doomed bring-ups (taken ports) with a clear message before the
	// compose engine turns them into cryptic failures.
	if action == "up" || action == "update" {
		if problems := s.preflight(ctx, st); len(problems) > 0 {
			http.Error(w, "pre-flight failed:\n  "+strings.Join(problems, "\n  "), 409)
			return
		}
	}

	var stream io.ReadCloser
	switch action {
	case "up":
		stream, err = s.backendFor(st).Up(ctx, st)
	case "down":
		stream, err = s.backendFor(st).Down(ctx, st)
	case "update":
		stream, err = s.backendFor(st).Update(ctx, st)
	case "restart":
		stream, err = s.backendFor(st).Restart(ctx, st)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Record operator intent so start-on-boot can restore it after a
	// fjordd/host restart. Best-effort: a state write failure must not abort
	// the lifecycle op the user asked for. (update leaves it running.)
	if desired := map[string]string{"up": "running", "down": "stopped", "update": "running", "restart": "running"}[action]; desired != "" {
		if err := s.manager.SetDesiredState(name, desired); err != nil {
			log.Printf("%s %s: persist desired_state: %v", action, name, err)
		}
	}

	streamOutput(w, stream)
}
