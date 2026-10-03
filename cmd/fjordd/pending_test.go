package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/stack"
)

func TestErrorWatchAcrossReads(t *testing.T) {
	w := &errorWatch{}
	w.Write([]byte("recreating web\n[err"))
	w.Write([]byte("or] web was not given the saved changes\n"))
	if !w.seen {
		t.Error("[error] split across two writes was missed")
	}
	clean := &errorWatch{}
	clean.Write([]byte("[warn] recreate refused; retrying\ndone\n"))
	if clean.seen {
		t.Error("a [warn] counted as an error")
	}
}

func TestThenStreamRunsInOrder(t *testing.T) {
	started := false
	s := thenStream(io.NopCloser(strings.NewReader("first\n")), func() (io.ReadCloser, error) {
		started = true
		return io.NopCloser(strings.NewReader("second\n")), nil
	})
	buf := make([]byte, 6)
	n, _ := s.Read(buf)
	if started || string(buf[:n]) != "first\n" {
		t.Fatalf("second began before first ended (read %q)", buf[:n])
	}
	rest, _ := io.ReadAll(s)
	if !started || string(rest) != "second\n" {
		t.Errorf("then got %q", rest)
	}
}

// A failed apply leaves the changes pending; a clean one clears exactly what
// it applied.
func TestPendingClearedOnlyOnSuccess(t *testing.T) {
	s := &server{manager: stack.NewManager(t.TempDir())}
	if err := s.manager.Save(&stack.Stack{Name: "app", Compose: "services:\n  web:\n    image: x\n  db:\n    image: y\n"}); err != nil {
		t.Fatal(err)
	}
	if err := s.manager.EnsureState("app"); err != nil {
		t.Fatal(err)
	}
	s.recordPending("app", []string{"web", "db"})
	pending := func() []string { st, _ := s.manager.LoadState("app"); return st.PendingServices }
	if !slices.Equal(pending(), []string{"db", "web"}) {
		t.Fatalf("recorded %v", pending())
	}

	io.ReadAll(s.clearPendingAfter("app", []string{"web"}, io.NopCloser(strings.NewReader("\n[error] web was not given the saved changes\n"))))
	if !slices.Equal(pending(), []string{"db", "web"}) {
		t.Errorf("after a failed apply: %v, want both still pending", pending())
	}
	io.ReadAll(s.clearPendingAfter("app", []string{"web"}, io.NopCloser(strings.NewReader("recreated web\n"))))
	if !slices.Equal(pending(), []string{"db"}) {
		t.Errorf("after applying web: %v, want [db]", pending())
	}
	io.ReadAll(s.clearPendingAfter("app", nil, io.NopCloser(strings.NewReader("updated everything\n"))))
	if len(pending()) != 0 {
		t.Errorf("after a whole-stack update: %v, want none", pending())
	}
}

// The first [error] line is kept, whole, however the chunks fall.
func TestErrorWatchKeepsTheFirstLine(t *testing.T) {
	w := &errorWatch{}
	w.Write([]byte("pulling\n[err"))
	w.Write([]byte("or] pull: no space left "))
	w.Write([]byte("on device\n[error] a second one\n"))
	if got := w.Message(); got != "pull: no space left on device" {
		t.Errorf("message %q", got)
	}
	none := &errorWatch{}
	none.Write([]byte("all fine\n"))
	if none.Message() != "" {
		t.Errorf("no error, got %q", none.Message())
	}
}

// An action that ends in an [error] is remembered on the stack, with the
// reason; the next clean one forgets it.
func TestRecordOutcome(t *testing.T) {
	s := &server{manager: stack.NewManager(t.TempDir())}
	if err := s.manager.Save(&stack.Stack{Name: "app", Compose: "services:\n  web:\n    image: x\n"}); err != nil {
		t.Fatal(err)
	}
	if err := s.manager.EnsureState("app"); err != nil {
		t.Fatal(err)
	}
	failure := func() *stack.Failure { st, _ := s.manager.LoadState("app"); return st.LastFailure }
	out, _ := io.ReadAll(s.recordOutcome("app", "install", io.NopCloser(strings.NewReader("$ podman pull x\n[error] pull: no space left on device\n"))))
	if !strings.Contains(string(out), "[error] pull") {
		t.Errorf("the stream must pass through unchanged, got %q", out)
	}
	f := failure()
	if f == nil || f.Action != "install" || f.Message != "pull: no space left on device" || f.At.IsZero() {
		t.Fatalf("after a failed install: %+v", f)
	}
	io.ReadAll(s.recordOutcome("app", "up", io.NopCloser(strings.NewReader("$ podman start web\n"))))
	if failure() != nil {
		t.Errorf("a clean start must clear the failure, got %+v", failure())
	}
}

// Dismiss forgets the failure on disk, so it stays gone after a reload.
func TestDismissFailureForgetsIt(t *testing.T) {
	s := &server{manager: stack.NewManager(t.TempDir())}
	if err := s.manager.Save(&stack.Stack{Name: "app", Compose: "services:\n  web:\n    image: x\n"}); err != nil {
		t.Fatal(err)
	}
	if err := s.manager.EnsureState("app"); err != nil {
		t.Fatal(err)
	}
	st, _ := s.manager.LoadState("app")
	st.LastFailure = &stack.Failure{Action: "update", Message: "web is gone since the update"}
	if err := s.manager.SaveState("app", st); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handleStackRoutes(w, httptest.NewRequest(http.MethodPost, "/api/stacks/app/dismiss-failure", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if st, _ := s.manager.LoadState("app"); st.LastFailure != nil {
		t.Errorf("failure still recorded: %+v", st.LastFailure)
	}
	w = httptest.NewRecorder()
	s.handleStackRoutes(w, httptest.NewRequest(http.MethodPost, "/api/stacks/nope/dismiss-failure", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown stack: status %d, want 404", w.Code)
	}
}
