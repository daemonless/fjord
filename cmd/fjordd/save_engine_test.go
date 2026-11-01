package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

// A new stack saved without naming an engine is bound to the default one.
// Left unbound, every start failed: "no container engine is available".
func TestSaveBindsNewStackToDefaultEngine(t *testing.T) {
	s := &server{
		manager:   stack.NewManager(t.TempDir()),
		backends:  map[string]engine.Backend{"podman": engine.Unavailable("podman")},
		defEngine: "podman",
	}
	body := `{"compose":"services:\n  web:\n    image: x\n","env":""}`
	rec := httptest.NewRecorder()
	s.stackSave(rec, httptest.NewRequest(http.MethodPost, "/api/stacks/app/save", strings.NewReader(body)), "app")
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	st, err := s.manager.Get("app")
	if err != nil {
		t.Fatal(err)
	}
	if got := st.EngineName(); got != "podman" {
		t.Errorf("engine %q, want the default podman", got)
	}
}

// The editor's save during a delete is refused with 409 and writes nothing,
// rather than putting back the compose the delete is removing.
func TestSaveDuringDeleteIsRefused(t *testing.T) {
	s := &server{
		manager:   stack.NewManager(t.TempDir()),
		backends:  map[string]engine.Backend{"podman": engine.Unavailable("podman")},
		defEngine: "podman",
	}
	old := "services:\n  web:\n    image: x\n"
	s.manager.Save(&stack.Stack{Name: "app", Compose: old})
	s.manager.SetDesiredState("app", desiredDeleting)
	body := `{"compose":"services:\n  web:\n    image: y\n","env":""}`
	rec := httptest.NewRecorder()
	s.stackSave(rec, httptest.NewRequest(http.MethodPost, "/api/stacks/app/save", strings.NewReader(body)), "app")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "being deleted") {
		t.Errorf("save: %d %s", rec.Code, rec.Body)
	}
	if st, _ := s.manager.Get("app"); st.Compose != old {
		t.Errorf("written anyway:\n%s", st.Compose)
	}
}

// An editor copy loaded before a rollback pinned the image is refused, and
// the pin stays; a copy of what is on disk saves.
func TestSaveRefusesAStaleEditorCopy(t *testing.T) {
	s := &server{
		manager:   stack.NewManager(t.TempDir()),
		backends:  map[string]engine.Backend{"podman": engine.Unavailable("podman")},
		defEngine: "podman",
	}
	s.manager.Save(&stack.Stack{Name: "app", Compose: "services:\n  web:\n    image: x\n"})
	opened, _ := s.manager.Get("app")
	stale := composeHash(opened)
	s.manager.Update("app", func(st *stack.Stack) error { st.Compose = "services:\n  web:\n    image: x@sha256:abc\n"; return nil })
	save := func(base string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		body := `{"compose":"services:\n  web:\n    image: y\n","env":"","baseHash":"` + base + `"}`
		s.stackSave(rec, httptest.NewRequest(http.MethodPost, "/api/stacks/app/save", strings.NewReader(body)), "app")
		return rec
	}
	if rec := save(stale); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "since you opened it") {
		t.Errorf("stale: %d %s", rec.Code, rec.Body)
	}
	if st, _ := s.manager.Get("app"); !strings.Contains(st.Compose, "x@sha256:abc") {
		t.Errorf("pin written over:\n%s", st.Compose)
	}
	cur, _ := s.manager.Get("app")
	if rec := save(composeHash(cur)); rec.Code != http.StatusOK {
		t.Errorf("current: %d %s", rec.Code, rec.Body)
	}
}
