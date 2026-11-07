package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// daemonCtx ends when fjordd is told to stop. Work fjordd starts on its own
// (the wire check) runs under it, so its cleanup runs before the exit: a
// probe killed by os.Exit left its epair on the bridge and dhclient running.
var daemonCtx, stopDaemon = context.WithCancel(context.Background())

// stopping refuses new stack operations once a stop has begun (lockStack).
var stopping atomic.Bool

// stopWait bounds how long a stop waits for operations in flight.
var stopWait = 60 * time.Second

var (
	httpServersMu sync.Mutex
	httpServers   []*http.Server
)

func registerHTTPServer(srv *http.Server) {
	if srv == nil {
		return
	}
	httpServersMu.Lock()
	httpServers = append(httpServers, srv)
	httpServersMu.Unlock()
}

func shutdownHTTPServers(wait time.Duration) {
	httpServersMu.Lock()
	srvs := append([]*http.Server(nil), httpServers...)
	httpServersMu.Unlock()
	if len(srvs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	var wg sync.WaitGroup
	for _, srv := range srvs {
		wg.Add(1)
		go func(s *http.Server) {
			defer wg.Done()
			_ = s.Shutdown(ctx)
		}(srv)
	}
	wg.Wait()
}

// stopOnSignal stops fjordd cleanly on SIGTERM or an interrupt (review 2.1).
// It used to close the terminals and os.Exit at once: an install or up still
// running carried on unsupervised, and the next fjordd's start-on-boot ran a
// second one beside it. Now nothing new starts, health watches end (as Apply
// ends one: the stack is saved as it is), and the operations still running
// get stopWait to finish.
func stopOnSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	go func() {
		got := <-sig
		log.Printf("%v: stopping", got)
		stopping.Store(true)
		stopDaemon()
		closeShells(5 * time.Second)
		if left := waitForOperations(stopWait); len(left) > 0 {
			log.Printf("stopping with %v still busy after %s", left, stopWait)
		}
		shutdownHTTPServers(5 * time.Second)
		os.Exit(0)
	}()
}

// waitForOperations ends every health watch and waits until no stack is
// locked, or wait passes. It returns the stacks still busy.
func waitForOperations(wait time.Duration) []string {
	deadline := time.Now().Add(wait)
	logged := false
	for {
		var busy []string
		lifecycleLocks.Range(func(k, v any) bool {
			name := k.(string)
			if op := loadBusy(name); op != nil {
				op.mu.Lock()
				stop := op.stop
				op.mu.Unlock()
				if stop != nil {
					stop()
				}
			}
			m := v.(*sync.Mutex)
			if !m.TryLock() {
				busy = append(busy, name)
				return true
			}
			m.Unlock()
			return true
		})
		if len(busy) == 0 || time.Now().After(deadline) {
			sort.Strings(busy)
			return busy
		}
		if !logged {
			sort.Strings(busy)
			log.Printf("waiting up to %s for %v", wait, busy)
			logged = true
		}
		time.Sleep(100 * time.Millisecond)
	}
}
