package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
)

// A refused action says what the stack is doing and for how long: a bare
// "another operation is running" left a Delete refused with no way to tell a
// pull still going from one stuck (immich, 2026-10-04).
func TestAnotherOperationSaysWhatAndHowLong(t *testing.T) {
	busyOps.Store("t-busy", &busyOp{action: "up", since: time.Now().Add(-3*time.Minute - 12*time.Second)})
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

// holdWatching takes name's lock as an update would and gets down to its
// health watch; the watch ends when stopped, and the operation then lets go.
func holdWatching(t *testing.T, name string) (stopped chan struct{}) {
	t.Helper()
	unlock, ok := lockStackAs(name, "update")
	if !ok {
		t.Fatalf("lock %s", name)
	}
	stopped = make(chan struct{})
	var once sync.Once
	engine.WatchStarted(watchable(context.Background(), name), func() { once.Do(func() { close(stopped) }) })
	go func() {
		<-stopped
		time.Sleep(100 * time.Millisecond) // the operation's tail
		unlock()
	}()
	return stopped
}

// Down to its watch, a stack is idle to the page, and the next action ends
// the watch and runs instead of being refused.
func TestActionTakesOverAWatch(t *testing.T) {
	stopped := holdWatching(t, "t-watch")
	if b := busyWith("t-watch"); b != "" {
		t.Errorf("busyWith during the watch: %q, want idle", b)
	}
	unlock, ok := lockStackAs("t-watch", "down")
	if !ok {
		t.Fatal("refused an action during the watch")
	}
	defer unlock()
	select {
	case <-stopped:
	default:
		t.Error("the watch was not ended")
	}
	if b := busyWith("t-watch"); b != "down" {
		t.Errorf("busyWith after the take-over: %q", b)
	}
}

// Anything else still refuses at once: a click must not queue behind a pull.
func TestActionRefusedBeforeTheWatch(t *testing.T) {
	unlock, ok := lockStackAs("t-pull", "update")
	if !ok {
		t.Fatal("lock")
	}
	defer unlock()
	start := time.Now()
	if _, ok := lockStackAs("t-pull", "down"); ok {
		t.Fatal("took over an update still pulling")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("refusal took %s", d)
	}
}

// Two actions during one watch: one takes over, the other is refused rather
// than waiting behind it.
func TestOnlyOneActionTakesOver(t *testing.T) {
	holdWatching(t, "t-two")
	type res struct {
		unlock func()
		ok     bool
	}
	out := make(chan res, 2)
	for _, a := range []string{"down", "restart"} {
		go func() {
			u, ok := lockStackAs("t-two", a)
			out <- res{u, ok}
		}()
	}
	won := 0
	for range 2 {
		select {
		case r := <-out:
			if r.ok {
				won++
				defer r.unlock()
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a taker was still waiting")
		}
	}
	if won != 1 {
		t.Errorf("%d actions took over, want 1", won)
	}
}
