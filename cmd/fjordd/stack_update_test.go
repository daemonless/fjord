package main

import (
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
