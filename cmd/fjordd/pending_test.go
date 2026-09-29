package main

import (
	"io"
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
