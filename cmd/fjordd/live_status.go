package main

import (
	"context"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

// What the pages show must never wait on the engine. podman holds its
// storage lock while a pull writes a layer, and every container list waits
// behind it: on netlab `podman ps` froze 300 s during an immich-ml pull (52 s
// on NVMe), and a user's stack page took ~30 s to open during updates. The
// event loop meanwhile timed out and published "unknown", then the real
// state again -- a stack that could not settle on a state.
//
// So the pages read status through liveStatus: it waits a short while for
// the engine, joins a call already running for the stack instead of queueing
// another behind the lock, and otherwise answers with the last status the
// engine gave, marked with StaleSince. Actions (delete, preflight, address
// checks) still ask the engine directly: they need the truth, not speed.

const (
	statusWait    = 2 * time.Second // how long a page waits for the engine
	statusTimeout = 5 * time.Minute // how long one engine call may run at all
)

type knownStatus struct {
	status engine.StackStatus
	at     time.Time
}

type liveStatus struct {
	mu       sync.Mutex
	last     map[string]knownStatus   // stack -> the engine's last answer
	inflight map[string]chan struct{} // stack -> closed when the running call ends
}

func newLiveStatus() *liveStatus {
	return &liveStatus{last: map[string]knownStatus{}, inflight: map[string]chan struct{}{}}
}

// get returns the stack's status, waiting at most wait for the engine.
func (l *liveStatus) get(be engine.Backend, st *stack.Stack, wait time.Duration) engine.StackStatus {
	l.mu.Lock()
	done, running := l.inflight[st.Name]
	if !running {
		done = make(chan struct{})
		l.inflight[st.Name] = done
		go l.ask(be, st, done)
	}
	l.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	k, ok := l.last[st.Name]
	select {
	case <-done:
		return k.status
	default:
	}
	// Still waiting on the engine: what it said last, and since when.
	if !ok {
		return engine.StackStatus{State: "unknown", StaleSince: time.Now()}
	}
	k.status.StaleSince = k.at
	return k.status
}

// ask runs one engine call and records its answer. The call is not tied to
// the page that started it: one that gives up after statusWait must not throw
// away an answer the next page can use.
func (l *liveStatus) ask(be engine.Backend, st *stack.Stack, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	status, err := be.Status(ctx, st)
	if err != nil {
		status = engine.StackStatus{State: "unknown"}
	}
	l.mu.Lock()
	l.last[st.Name] = knownStatus{status: status, at: time.Now()}
	delete(l.inflight, st.Name)
	l.mu.Unlock()
	close(done)
}

// forget drops a deleted stack, so a new one with its name starts clean.
func (l *liveStatus) forget(name string) {
	l.mu.Lock()
	delete(l.last, name)
	l.mu.Unlock()
}
