package main

import (
	"testing"

	"github.com/daemonless/fjord/pkg/stack"
)

// Another stack set to run claims its ports from its config, with no live
// status needed: zensical-2 started on vikunja's 3456 before the status
// arrived. A stopped stack, or one with an address of its own, claims none.
func TestOtherStackPorts(t *testing.T) {
	s := &server{manager: stack.NewManager(t.TempDir())}
	add := func(name, compose, desired string) {
		t.Helper()
		if err := s.manager.Save(&stack.Stack{Name: name, Compose: compose, Env: "WEB_PORT=3456\n"}); err != nil {
			t.Fatal(err)
		}
		if err := s.manager.SaveState(name, &stack.State{DesiredState: desired}); err != nil {
			t.Fatal(err)
		}
	}
	add("vikunja", "services:\n  vikunja:\n    image: v\n    ports:\n      - \"${WEB_PORT}:3456\"\n", "running")
	add("old", "services:\n  old:\n    image: o\n    ports:\n      - \"8080:80\"\n", "stopped")
	got := s.otherStackPorts(&stack.Stack{Name: "zensical-2"})
	if got["3456/tcp"] != "vikunja" || len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	if mine := s.otherStackPorts(&stack.Stack{Name: "vikunja"}); len(mine) != 0 {
		t.Fatalf("its own ports: %v", mine)
	}
}
