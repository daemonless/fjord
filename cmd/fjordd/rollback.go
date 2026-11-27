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
	// Rollback pins a service's image in the compose (and, on AppJail, the
	// director) and updates just that service. An engine that cannot take a
	// pin records nothing and offers no rollback.
	if !s.backendFor(st).Capabilities().PinServices {
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
	now := time.Now().UTC().Format(time.RFC3339)
	err = s.manager.UpdateState(st.Name, func(state *stack.State) *stack.State {
		if state == nil {
			return nil
		}
		if state.Rollback == nil {
			state.Rollback = map[string]stack.RollbackImage{}
		}
		// After a failed update the running image is the one that failed: a
		// retry recording it lost the version that worked (review 2.5).
		failed := state.LastFailure != nil && state.LastFailure.Action == "update"
		for _, ri := range ris {
			if ri.Digest == "" || ri.Ref == "" || (len(services) > 0 && !slices.Contains(services, ri.Service)) {
				continue
			}
			if _, kept := state.Rollback[ri.Service]; kept && failed {
				continue
			}
			state.Rollback[ri.Service] = stack.RollbackImage{
				Compose: composeImage[ri.Service], Ref: ri.Ref, Digest: ri.Digest, At: now,
			}
		}
		return state
	})
	if err != nil {
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
	body, _ := json.Marshal(map[string][]string{"services": req.Services})
	r2 := r.WithContext(context.WithValue(r.Context(), isRollbackKey{}, true))
	r2.Body = io.NopCloser(bytes.NewReader(body))
	s.lifecycle(w, r2, name, "update", func() error {
		st, err := s.manager.Get(name)
		if err != nil {
			return httpError{404, "Stack not found"}
		}
		if problems := s.preflight(r.Context(), st); len(problems) > 0 {
			return httpError{http.StatusConflict, "pre-flight failed:\n  " + strings.Join(problems, "\n  ")}
		}
		_, err = s.manager.Update(name, func(st *stack.Stack) error {
			state, err := s.manager.LoadState(name)
			if err != nil || state == nil {
				return httpError{500, "no state for " + name}
			}
			for _, svc := range req.Services {
				rb, ok := state.Rollback[svc]
				if !ok {
					return httpError{400, fmt.Sprintf("%s has no earlier image recorded for %s", name, svc)}
				}
				if err := setServiceImage(st, svc, withoutDigest(rb.Ref)+"@"+rb.Digest); err != nil {
					return httpError{400, err.Error()}
				}
			}
			return nil
		})
		return err
	})
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
		Hold    string `json:"hold,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Service == "" {
		http.Error(w, `send {"service": "..."}`, 400)
		return
	}
	unlock, ok := lockStackTakingOver(name)
	if !ok {
		http.Error(w, anotherOperation(name), http.StatusConflict)
		return
	}
	defer unlock()
	if _, err := s.manager.Update(name, func(st *stack.Stack) error {
		cur := serviceImage(st, req.Service)
		if !strings.Contains(cur, "@") {
			return httpError{400, req.Service + " is not pinned"}
		}
		restore := withoutDigest(cur)
		if state, _ := s.manager.LoadState(name); state != nil {
			if rb, ok := state.Rollback[req.Service]; ok && rb.Compose != "" &&
				composepkg.ExpandEnv(rb.Compose, st.EnvMap()) == restore {
				restore = rb.Compose
			}
		}
		if err := setServiceImage(st, req.Service, restore); err != nil {
			return httpError{400, err.Error()}
		}
		return nil
	}); err != nil {
		replyErr(w, err)
		return
	}
	s.fleet.forget(name)
	if req.Hold != "" {
		holdKey := holdKeyForImage(req.Service, req.Hold)
		if holdKey != "" {
			_ = s.manager.UpdateState(name, func(st *stack.State) *stack.State {
				if st == nil {
					return nil
				}
				if st.AutoHold == nil {
					st.AutoHold = map[string]string{}
				}
				st.AutoHold[req.Service] = holdKey
				return st
			})
		}
	} else {
		s.clearAutoHold(name, []string{req.Service})
	}
	w.WriteHeader(http.StatusNoContent)
}

// holdKeyForImage converts a pinned image ref (or JSON service->image map)
// into the key auto-update's candidateKey compares against: repo:tag for a
// versioned tag, or the bare sha256:... digest for a moved :latest/untagged
// image.
func holdKeyForImage(service, img string) string {
	if strings.HasPrefix(img, "{") {
		var m map[string]string
		if json.Unmarshal([]byte(img), &m) == nil {
			img = m[service]
		}
	}
	if img == "" {
		return ""
	}
	ref := withoutDigest(img)
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash && colon >= 0 {
		tag := ref[colon+1:]
		if tag != "" && tag != "latest" {
			return ref
		}
	}
	if at := strings.LastIndex(img, "@"); at >= 0 {
		return img[at+1:]
	}
	return img
}

// stackAutoRollback answers an automatic update that failed and was rolled
// back: POST .../auto-rollback {"answer": "keep" | "retry"}. Keep leaves it
// on what it runs, and the update that failed stays held. Try again gives
// the services back their tag, or the version they were moving to, and
// updates them now, as the Update button does.
func (s *server) stackAutoRollback(w http.ResponseWriter, r *http.Request, name string) {
	var req struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Answer != "keep" && req.Answer != "retry") {
		http.Error(w, `send {"answer": "keep" | "retry"}`, 400)
		return
	}
	state, err := s.manager.LoadState(name)
	if err != nil || state == nil || state.AutoRollback == nil {
		http.Error(w, "no failed automatic update to answer", 404)
		return
	}
	ar := *state.AutoRollback
	if req.Answer == "keep" {
		if err := s.manager.UpdateState(name, func(st *stack.State) *stack.State {
			if st == nil {
				return nil
			}
			st.AutoRollback = nil
			return st
		}); err != nil {
			replyErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	unlock, ok := lockStackAs(name, "update")
	if !ok {
		http.Error(w, anotherOperation(name), http.StatusConflict)
		return
	}
	if _, err := s.manager.Update(name, func(st *stack.Stack) error {
		for _, svc := range ar.Services {
			cur := serviceImage(st, svc)
			if ar.Tags[svc] != "" || !strings.Contains(cur, "@") {
				continue
			}
			restore := withoutDigest(cur)
			if rb, ok := state.Rollback[svc]; ok && rb.Compose != "" &&
				composepkg.ExpandEnv(rb.Compose, st.EnvMap()) == restore {
				restore = rb.Compose
			}
			if err := setServiceImage(st, svc, restore); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		unlock()
		replyErr(w, err)
		return
	}
	for svc, tag := range ar.Tags {
		if err := s.retagForUpdate(name, svc, tag); err != nil {
			unlock()
			replyErr(w, err)
			return
		}
	}
	s.clearAutoHold(name, ar.Services)
	if err := s.manager.UpdateState(name, func(st *stack.State) *stack.State {
		if st == nil {
			return nil
		}
		st.AutoRollback = nil
		return st
	}); err != nil {
		log.Printf("try again %s: %v", name, err)
	}
	s.updateDetached(name, unlock, ar.Services, triggerOf(r))
	w.WriteHeader(http.StatusAccepted)
}

// withoutDigest drops an @sha256:... from an image ref.
func withoutDigest(ref string) string {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		return ref[:at]
	}
	return ref
}

// setServiceImage makes a service run ref: written in the compose where the
// compose has the service, the director following on AppJail; in the
// director alone for a director project whose compose does not name it
// (an AppJail stack made from a director file, with no compose).
func setServiceImage(st *stack.Stack, service, ref string) error {
	if composeImage(st.Compose, service) != "" {
		compose, err := composepkg.SetServiceImage(st.Compose, service, ref)
		if err != nil {
			return err
		}
		st.Compose = compose
		return directorFollowsCompose(st, []string{service})
	}
	if strings.TrimSpace(st.Director) == "" {
		return fmt.Errorf("no service %q", service)
	}
	director, err := setDirectorImage(st.Director, service, ref, makejailRepoOf(st))
	if err != nil {
		return err
	}
	st.Director = director
	return nil
}

// serviceImage is the image a service is set to run: the compose's, or on a
// director project without one, the director's.
func serviceImage(st *stack.Stack, service string) string {
	if img := composeImage(st.Compose, service); img != "" {
		return img
	}
	return directorImage(st, service)
}

// composeImage is a service's image as the compose writes it, "" when the
// compose has no such service.
func composeImage(compose, service string) string {
	list, _ := composepkg.ServiceImageList(compose)
	for _, si := range list {
		if si.Service == service {
			return si.Image
		}
	}
	return ""
}
