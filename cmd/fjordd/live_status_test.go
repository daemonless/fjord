package main

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

// slowEngine answers Status with state, but only once gate lets it through:
// podman behind the storage lock while a pull writes a layer.
type slowEngine struct {
	engine.Backend
	gate  chan struct{}
	calls atomic.Int32
	state atomic.Value
}

func (e *slowEngine) Status(ctx context.Context, _ *stack.Stack) (engine.StackStatus, error) {
	e.calls.Add(1)
	select {
	case <-e.gate:
	case <-ctx.Done():
		return engine.StackStatus{}, ctx.Err()
	}
	return engine.StackStatus{State: e.state.Load().(string)}, nil
}

func TestLiveStatusNeverWaitsOnABusyEngine(t *testing.T) {
	open := make(chan struct{})
	close(open)
	e := &slowEngine{gate: open}
	e.state.Store("running")
	st := &stack.Stack{Name: "immich"}
	l := newLiveStatus()

	// The engine answers: fresh, nothing stale.
	if got := l.get(e, st, time.Second); got.State != "running" || !got.StaleSince.IsZero() {
		t.Fatalf("fresh: %+v", got)
	}

	// A pull takes the lock: every page gets the last state at once, marked
	// stale, and five pages asking together send ONE call, not five queued
	// behind the lock.
	e.gate = make(chan struct{})
	e.state.Store("stopped")
	before := e.calls.Load()
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			got := l.get(e, st, 50*time.Millisecond)
			if time.Since(start) > time.Second {
				t.Errorf("waited %s on a busy engine", time.Since(start))
			}
			if got.State != "running" || got.StaleSince.IsZero() {
				t.Errorf("busy: want the last state, marked stale; got %+v", got)
			}
		}()
	}
	wg.Wait()
	if n := e.calls.Load() - before; n != 1 {
		t.Fatalf("%d engine calls for 5 pages, want 1", n)
	}

	// The lock is released: the call that was running lands, and the next
	// page gets the engine's real answer.
	close(e.gate)
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := l.get(e, st, 100*time.Millisecond)
		if got.State == "stopped" && got.StaleSince.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never got the fresh state: %+v", got)
		}
	}
}

// A stack the engine has never answered for reads "unknown", still marked
// stale so the page can say why.
func TestLiveStatusUnknownBeforeFirstAnswer(t *testing.T) {
	e := &slowEngine{gate: make(chan struct{})}
	e.state.Store("running")
	got := newLiveStatus().get(e, &stack.Stack{Name: "new"}, 20*time.Millisecond)
	if got.State != "unknown" || got.StaleSince.IsZero() {
		t.Fatalf("got %+v", got)
	}
	close(e.gate)
}

type containerSliceEngine struct {
	engine.Backend
	gate   chan struct{}
	status engine.StackStatus
}

func (e *containerSliceEngine) Status(ctx context.Context, _ *stack.Stack) (engine.StackStatus, error) {
	select {
	case <-e.gate:
	case <-ctx.Done():
		return engine.StackStatus{}, ctx.Err()
	}
	return e.status, nil
}

func TestLiveStatusConcurrentGetAndMutate(t *testing.T) {
	open := make(chan struct{})
	close(open)
	e := &containerSliceEngine{
		gate: open,
		status: engine.StackStatus{
			State: "running",
			Containers: []engine.ContainerStatus{{
				Name:      "app_1",
				ID:        "c1",
				Service:   "app",
				State:     "running",
				Restarts:  3,
				Ports:     []engine.Port{{HostPort: 8080, ContainerPort: 80}},
				Exposed:   []int{80},
				Addresses: map[string]string{"lan": "10.0.0.2"},
				MACs:      map[string]string{"lan": "02:00:00:00:00:01"},
			}},
		},
	}
	st := &stack.Stack{Name: "app"}
	l := newLiveStatus()

	// Seed the cache and verify caller mutations do not leak back into it.
	first := l.get(e, st, time.Second)
	first.Containers[0].Detail = "mutated by caller"
	first.Containers[0].Addresses["lan"] = "0.0.0.0"

	// Hold the next engine call so subsequent get() calls read from l.last.
	e.gate = make(chan struct{})
	defer close(e.gate)

	second := l.get(e, st, 5*time.Millisecond)
	if second.Containers[0].Detail != "" {
		t.Fatalf("cached Detail was mutated: got %q, want empty", second.Containers[0].Detail)
	}
	if second.Containers[0].Addresses["lan"] != "10.0.0.2" {
		t.Fatalf("cached Addresses was mutated: got %q", second.Containers[0].Addresses["lan"])
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				got := l.get(e, st, time.Millisecond)
				if len(got.Containers) > 0 {
					got.Containers[0].Detail = "crash-looping: restarted 3 times"
					got.Containers[0].Ports[0].HostPort = 9090
					got.Containers[0].Addresses["lan"] = "10.0.0.99"
				}
				_, _ = json.Marshal(got)
			}
		}()
	}
	wg.Wait()
}
