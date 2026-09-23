package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/stack"
)

// isRollbackKey marks an update that IS a rollback, which must not record
// itself: the record would then name the image being rolled away from, and
// "Roll back" would put it straight back.
type isRollbackKey struct{}

// recordRollback saves, for each service about to be updated, the image its
// container runs now. services empty = all of them.
func (s *server) recordRollback(ctx context.Context, st *stack.Stack, services []string) {
	// Rollback pins a service's image in the compose and updates just that
	// service. An engine that updates the whole project (appjail director,
	// whose spec names makejails) can do neither, so nothing is recorded and
	// no rollback is offered.
	if !s.backendFor(st).Capabilities().UpdateServices {
		return
	}
	ris, err := s.backendFor(st).RunningImages(ctx, st)
	if err != nil || len(ris) == 0 {
		return
	}
	composeImage := map[string]string{}
	if list, err := composepkg.ServiceImageList(st.Compose); err == nil {
		for _, si := range list {
			composeImage[si.Service] = si.Image
		}
	}
	state, err := s.manager.LoadState(st.Name)
	if err != nil || state == nil {
		return
	}
	if state.Rollback == nil {
		state.Rollback = map[string]stack.RollbackImage{}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, ri := range ris {
		if ri.Digest == "" || ri.Ref == "" || (len(services) > 0 && !slices.Contains(services, ri.Service)) {
			continue
		}
		state.Rollback[ri.Service] = stack.RollbackImage{
			Compose: composeImage[ri.Service], Ref: ri.Ref, Digest: ri.Digest, At: now,
		}
	}
	if err := s.manager.SaveState(st.Name, state); err != nil {
		log.Printf("rollback record for %s: %v", st.Name, err)
	}
}

// stackRollback returns services to the image their last update replaced:
// POST .../rollback {"services": [...]}.
//
// Each is pinned to the digest it ran before -- repo:tag@sha256:... -- and
// then updated like any other service, so dependents, the recreate check and
// the health watch all apply. The digest is fetched from the registry: the
// old image is gone from the host after `podman image prune -af`.
//
// It does not undo what the new version did to its data. An app that
// migrated its database on startup is now older code on a newer schema.
func (s *server) stackRollback(w http.ResponseWriter, r *http.Request, name string) {
	var req struct {
		Services []string `json:"services"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Services) == 0 {
		http.Error(w, `send {"services": [...]}`, 400)
		return
	}
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	state, err := s.manager.LoadState(name)
	if err != nil || state == nil {
		http.Error(w, "no state for "+name, 500)
		return
	}
	compose := st.Compose
	for _, svc := range req.Services {
		rb, ok := state.Rollback[svc]
		if !ok {
			http.Error(w, fmt.Sprintf("%s has no earlier image recorded for %s", name, svc), 400)
			return
		}
		if compose, err = composepkg.SetServiceImage(compose, svc, withoutDigest(rb.Ref)+"@"+rb.Digest); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	st.Compose = compose
	if err := s.manager.Save(st); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	body, _ := json.Marshal(map[string][]string{"services": req.Services})
	r2 := r.WithContext(context.WithValue(r.Context(), isRollbackKey{}, true))
	r2.Body = io.NopCloser(bytes.NewReader(body))
	s.stackLifecycle(w, r2, name, "update")
}

// stackUnpin lets a pinned service take updates again: POST .../unpin
// {"service": "..."}. Nothing is recreated -- the running bytes do not
// change -- so the next update check simply offers the update again.
//
// The compose gets back what the operator wrote (immich's
// "immich-server:${IMMICH_TAG:-latest}") when the rollback record still
// matches it; otherwise the pin is just dropped from the ref.
func (s *server) stackUnpin(w http.ResponseWriter, r *http.Request, name string) {
	var req struct {
		Service string `json:"service"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Service == "" {
		http.Error(w, `send {"service": "..."}`, 400)
		return
	}
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	list, _ := composepkg.ServiceImageList(st.Compose)
	cur := ""
	for _, si := range list {
		if si.Service == req.Service {
			cur = si.Image
		}
	}
	if !strings.Contains(cur, "@") {
		http.Error(w, req.Service+" is not pinned", 400)
		return
	}
	restore := withoutDigest(cur)
	if state, _ := s.manager.LoadState(name); state != nil {
		if rb, ok := state.Rollback[req.Service]; ok && rb.Compose != "" &&
			composepkg.ExpandEnv(rb.Compose, st.EnvMap()) == restore {
			restore = rb.Compose
		}
	}
	compose, err := composepkg.SetServiceImage(st.Compose, req.Service, restore)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	st.Compose = compose
	if err := s.manager.Save(st); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.fleet.forget(name)
	w.WriteHeader(http.StatusNoContent)
}

// withoutDigest drops an @sha256:... from an image ref.
func withoutDigest(ref string) string {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		return ref[:at]
	}
	return ref
}
