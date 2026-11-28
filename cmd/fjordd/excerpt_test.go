package main

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/history"
	"github.com/daemonless/fjord/pkg/stack"
)

func TestRecordOutcomeSavesExcerptOnFailedUpdate(t *testing.T) {
	root := t.TempDir()
	s := &server{
		manager:   stack.NewManager(root + "/stacks"),
		fjordRoot: root,
		hist:      history.New(root),
	}
	_ = s.manager.Save(&stack.Stack{Name: "app", Compose: "services:\n  web:\n    image: x\n"})
	_ = s.manager.UpdateState("app", func(*stack.State) *stack.State {
		return &stack.State{SchemaVersion: 1, DesiredState: "running"}
	})

	// 1. Short failed update output (<= 20 lines) is kept whole.
	shortOut := "pulling image...\nchecking manifest...\n[error] manifest unknown\n"
	n1 := newNote(history.Update, history.ByYou)
	r1 := s.recordOutcome("app", "update", n1, io.NopCloser(strings.NewReader(shortOut)))
	_, _ = io.ReadAll(r1)
	_ = r1.Close()

	entries := s.hist.Stack("app", 10)
	if len(entries) != 1 || entries[0].Result != history.Failed {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Message != "manifest unknown" {
		t.Fatalf("message = %q, want manifest unknown", entries[0].Message)
	}
	if entries[0].Excerpt != strings.TrimRight(shortOut, "\n") {
		t.Fatalf("short excerpt = %q, want %q", entries[0].Excerpt, strings.TrimRight(shortOut, "\n"))
	}

	// 2. Long failed update output (> 20 lines) keeps first 10 + omitted marker + last 10.
	var longLines []string
	for i := 1; i <= 30; i++ {
		if i == 30 {
			longLines = append(longLines, "[error] container exited 1")
		} else {
			longLines = append(longLines, fmt.Sprintf("step %d output", i))
		}
	}
	n2 := newNote(history.Update, history.ByYou)
	r2 := s.recordOutcome("app", "update", n2, io.NopCloser(strings.NewReader(strings.Join(longLines, "\n")+"\n")))
	_, _ = io.ReadAll(r2)
	_ = r2.Close()

	entries = s.hist.Stack("app", 10)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	gotLines := strings.Split(entries[0].Excerpt, "\n")
	if len(gotLines) != 21 || gotLines[0] != "step 1 output" || gotLines[10] != "... (10 lines omitted) ..." || gotLines[20] != "[error] container exited 1" {
		t.Fatalf("long excerpt unexpected:\n%s", entries[0].Excerpt)
	}

	// 3. Succeeding update stores no excerpt.
	n3 := newNote(history.Update, history.ByYou)
	r3 := s.recordOutcome("app", "update", n3, io.NopCloser(strings.NewReader("pulled ok\nstarted ok\n")))
	_, _ = io.ReadAll(r3)
	_ = r3.Close()

	entries = s.hist.Stack("app", 10)
	if len(entries) != 3 || entries[0].Result != history.OK || entries[0].Excerpt != "" {
		t.Fatalf("ok entry should have no excerpt: %+v", entries[0])
	}
}
