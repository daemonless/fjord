package main

import "testing"

// Only a running container taken over by replace is started by fjord: one
// adopted beside its original keeps running as the original, and a stopped
// one stays stopped, as it was.
func TestReplacedWasRunning(t *testing.T) {
	if !replacedWasRunning(true, "running") {
		t.Fatal("replace of a running container: not started")
	}
	for _, c := range []struct {
		replace bool
		state   string
	}{{false, "running"}, {true, "exited"}, {true, "created"}, {true, ""}} {
		if replacedWasRunning(c.replace, c.state) {
			t.Fatalf("replace=%v state=%q: started", c.replace, c.state)
		}
	}
}
