package main

import (
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

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

// closeShellsOnExit closes every open terminal when fjordd is told to stop,
// then exits. Not a graceful HTTP shutdown: event streams and terminals never
// end on their own, so waiting for them would only delay the stop.
func closeShellsOnExit() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	go func() {
		got := <-sig
		openShells.Lock()
		shells := make([]engine.ExecSession, 0, len(openShells.m))
		for s := range openShells.m {
			shells = append(shells, s)
		}
		openShells.Unlock()
		if len(shells) > 0 {
			log.Printf("%v: closing %d open terminal(s)", got, len(shells))
		}
		var wg sync.WaitGroup
		for _, s := range shells {
			wg.Add(1)
			go func() { defer wg.Done(); s.Close() }()
		}
		wg.Wait()
		os.Exit(0)
	}()
}
