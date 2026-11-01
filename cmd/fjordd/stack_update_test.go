package main

import (
	"context"
	"github.com/daemonless/fjord/pkg/engine"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/stack"
)

func unpinFixture(t *testing.T, compose string) *server {
	t.Helper()
	s := &server{manager: stack.NewManager(t.TempDir())}
	if err := s.manager.Save(&stack.Stack{Name: "app", Compose: compose, Env: "TAG=2.0\n"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func postUnpin(s *server, name, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.stackUnpin(rec, httptest.NewRequest(http.MethodPost, "/api/stacks/"+name+"/unpin", strings.NewReader(body)), name)
	return rec
}

// Unpin drops the digest, puts back what the operator wrote when the
// rollback record still matches it, and leaves the .env as it was.
func TestStackUnpin(t *testing.T) {
	s := unpinFixture(t, "services:\n  web:\n    image: repo/web:2.0@sha256:abc\n")
	s.manager.UpdateState("app", func(st *stack.State) *stack.State {
		return &stack.State{SchemaVersion: 1, Rollback: map[string]stack.RollbackImage{"web": {Compose: "repo/web:${TAG}"}}}
	})
	if rec := postUnpin(s, "app", `{"service":"web"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	st, _ := s.manager.Get("app")
	if !strings.Contains(st.Compose, "image: repo/web:${TAG}") || st.Env != "TAG=2.0\n" {
		t.Errorf("compose:\n%s\nenv %q", st.Compose, st.Env)
	}
}

// What unpin refuses, with the status each gets.
func TestStackUnpinRefuses(t *testing.T) {
	s := unpinFixture(t, "services:\n  web:\n    image: repo/web:2.0\n")
	if rec := postUnpin(s, "app", `{"service":"web"}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "web is not pinned") {
		t.Errorf("not pinned: %d %s", rec.Code, rec.Body)
	}
	if rec := postUnpin(s, "gone", `{"service":"web"}`); rec.Code != 404 {
		t.Errorf("no such stack: %d %s", rec.Code, rec.Body)
	}
}

// Set tag looks the version up before it writes; a save that lands in
// between is not written over.
func TestUnchangedSince(t *testing.T) {
	snap := &stack.Stack{Name: "app", Compose: "a", Env: "e"}
	if err := unchangedSince(&stack.Stack{Name: "app", Compose: "a", Env: "e"}, snap); err != nil {
		t.Errorf("unchanged refused: %v", err)
	}
	err := unchangedSince(&stack.Stack{Name: "app", Compose: "b", Env: "e"}, snap)
	if he, ok := err.(httpError); !ok || he.code != http.StatusConflict {
		t.Errorf("changed: %v", err)
	}
}

// A rollback on a busy stack is refused before it pins anything. It wrote
// the pin first, and the 409 left the compose on the old image while the
// container still ran the new one.
func TestRollbackOnABusyStackWritesNothing(t *testing.T) {
	compose := "services:\n  web:\n    image: repo/web:2.0\n"
	s := unpinFixture(t, compose)
	s.manager.UpdateState("app", func(st *stack.State) *stack.State {
		return &stack.State{SchemaVersion: 1, Rollback: map[string]stack.RollbackImage{
			"web": {Ref: "repo/web:1.0", Digest: "sha256:abc"},
		}}
	})
	unlock, ok := lockStackAs("app", "up")
	if !ok {
		t.Fatal("lock")
	}
	defer unlock()
	rec := httptest.NewRecorder()
	s.stackRollback(rec, httptest.NewRequest(http.MethodPost, "/api/stacks/app/rollback", strings.NewReader(`{"services":["web"]}`)), "app")
	if rec.Code != http.StatusConflict {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if st, _ := s.manager.Get("app"); st.Compose != compose {
		t.Errorf("pinned anyway:\n%s", st.Compose)
	}
}

// runningEngine reports one running image and can pin, for recordRollback.
type runningEngine struct {
	engine.Backend
	running []engine.RunningImage
}

func (e runningEngine) Capabilities() engine.Capabilities {
	return engine.Capabilities{PinServices: true}
}
func (e runningEngine) RunningImages(context.Context, *stack.Stack) ([]engine.RunningImage, error) {
	return e.running, nil
}

// Update v1 -> v2 failed, and Update is pressed again: the record must still
// name v1, the version that worked. It named v2, the broken one, so "Roll
// back" went nowhere (review 2.5).
func TestRetriedUpdateKeepsTheWorkingRollback(t *testing.T) {
	s := unpinFixture(t, "services:\n  web:\n    image: repo/web:2.0\n")
	s.backends = map[string]engine.Backend{"podman": runningEngine{engine.Unavailable("podman"),
		[]engine.RunningImage{{Service: "web", Ref: "repo/web:2.0", Digest: "sha256:v2"}}}}
	s.manager.SetEngine("app", "podman")
	s.manager.UpdateState("app", func(st *stack.State) *stack.State {
		return &stack.State{SchemaVersion: 1, Engine: "podman",
			Rollback:    map[string]stack.RollbackImage{"web": {Ref: "repo/web:1.0", Digest: "sha256:v1"}},
			LastFailure: &stack.Failure{Action: "update", Message: "web exited"}}
	})
	st, _ := s.manager.Get("app")
	s.recordRollback(context.Background(), st, []string{"web"})
	state, _ := s.manager.LoadState("app")
	if got := state.Rollback["web"].Digest; got != "sha256:v1" {
		t.Errorf("rollback now names %s, want the working sha256:v1", got)
	}
	// After a good update the record moves on as before.
	s.manager.UpdateState("app", func(st *stack.State) *stack.State { st.LastFailure = nil; return st })
	s.recordRollback(context.Background(), st, []string{"web"})
	if state, _ = s.manager.LoadState("app"); state.Rollback["web"].Digest != "sha256:v2" {
		t.Errorf("after a good update: %s", state.Rollback["web"].Digest)
	}
}
