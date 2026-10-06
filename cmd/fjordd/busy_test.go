package main

import (
	"strings"
	"testing"
	"time"
)

// A refused action says what the stack is doing and for how long: a bare
// "another operation is running" left a Delete refused with no way to tell a
// pull still going from one stuck (immich, 2026-10-04).
func TestAnotherOperationSaysWhatAndHowLong(t *testing.T) {
	busyOps.Store("t-busy", busyOp{"up", time.Now().Add(-3*time.Minute - 12*time.Second)})
	t.Cleanup(func() { busyOps.Delete("t-busy") })
	got := anotherOperation("t-busy")
	for _, want := range []string{"t-busy is starting", "for 3m12s", "wait for it to finish"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	if busyWith("t-busy") != "up" {
		t.Errorf("busyWith: %q", busyWith("t-busy"))
	}
}
