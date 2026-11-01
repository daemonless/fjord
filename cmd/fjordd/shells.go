package main

import (
	"log"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
)

// openShells are the terminal sessions this fjordd has open, so it can close
// them on the way out.
//
// Closing is what ends the shell's process: dropping the stream alone leaves
// it running, and podman then refuses to remove the container ("active exec
// sessions") -- an update of it fails. fjordd used to die on SIGTERM without
// running any of that, so every restart stranded whatever terminals were
// open. jupiter had seerr shells 23 hours old, from restarts that day.
var openShells = struct {
	sync.Mutex
	m map[engine.ExecSession]struct{}
}{m: map[engine.ExecSession]struct{}{}}

func trackShell(s engine.ExecSession) (untrack func()) {
	openShells.Lock()
	openShells.m[s] = struct{}{}
	openShells.Unlock()
	return func() {
		openShells.Lock()
		delete(openShells.m, s)
		openShells.Unlock()
	}
}

// closeShells closes every open terminal, waiting at most wait: a wedged
// engine must not keep fjordd from stopping.
func closeShells(wait time.Duration) {
	openShells.Lock()
	shells := make([]engine.ExecSession, 0, len(openShells.m))
	for s := range openShells.m {
		shells = append(shells, s)
	}
	openShells.Unlock()
	if len(shells) == 0 {
		return
	}
	log.Printf("closing %d open terminal(s)", len(shells))
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, s := range shells {
			wg.Add(1)
			go func() { defer wg.Done(); s.Close() }()
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(wait):
		log.Printf("terminals still closing after %s; stopping anyway", wait)
	}
}
