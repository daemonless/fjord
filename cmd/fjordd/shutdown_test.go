package main

import (
	"reflect"
	"testing"
	"time"
)

// Review 2.1: a stop ends health watches, waits for operations still
// running, and starts none of its own.
func TestWaitForOperations(t *testing.T) {
	t.Cleanup(func() { stopping.Store(false) })

	// A stack down to its health watch: the stop ends the watch, and the
	// operation then lets go.
	watched, _ := lockStackAs("t-watch", "up")
	stopped := make(chan struct{})
	loadBusy("t-watch").stop = func() {
		select {
		case <-stopped:
		default:
			close(stopped)
			go watched()
		}
	}
	// One still pulling holds on past the wait.
	pulling, _ := lockStackAs("t-pull", "installing")
	defer pulling()

	stopping.Store(true)
	if _, ok := lockStack("t-new"); ok {
		t.Error("a stack locked after the stop began")
	}
	if left := waitForOperations(300 * time.Millisecond); !reflect.DeepEqual(left, []string{"t-pull"}) {
		t.Errorf("still busy = %v, want [t-pull]", left)
	}
	select {
	case <-stopped:
	default:
		t.Error("the health watch was not ended")
	}
}
