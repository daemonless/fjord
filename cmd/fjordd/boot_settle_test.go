package main

import (
	"context"
	"slices"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
)

// settler is an engine that starts stacks itself at boot; the embedded
// Backend is nil because settleBoot only asks for BootSettle.
type settler struct {
	engine.Backend
	calls int
}

func (s *settler) BootSettle(context.Context) error { s.calls++; return nil }

// plain is an engine with no boot start of its own (podman).
type plain struct{ engine.Backend }

func TestSettleBootWaitsOncePerEngine(t *testing.T) {
	settled := map[string]bool{}
	aj := &settler{}
	for i := 0; i < 3; i++ { // three appjail stacks
		settleBoot(settled, "appjail", aj)
	}
	if aj.calls != 1 {
		t.Errorf("appjail's boot start waited on %d times, want once", aj.calls)
	}
	settleBoot(settled, "podman", plain{}) // must not panic or wait
	if settled["podman"] {
		t.Error("an engine without BootSettle was marked settled")
	}
}

// Stacks of engines that settle sort after the rest, the rest keeping their order.
func TestBoolOrderSortsSettlersLast(t *testing.T) {
	in := []bool{true, false, true, false}
	slices.SortStableFunc(in, boolOrder)
	if !slices.Equal(in, []bool{false, false, true, true}) {
		t.Errorf("got %v", in)
	}
}
