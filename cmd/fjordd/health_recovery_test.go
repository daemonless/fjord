package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

type recoveryFakeBackend struct {
	engine.Backend
	status   engine.StackStatus
	restarts int
	downs    int
}

func (f *recoveryFakeBackend) Capabilities() engine.Capabilities {
	return engine.Capabilities{}
}

func (f *recoveryFakeBackend) Status(context.Context, *stack.Stack) (engine.StackStatus, error) {
	return f.status, nil
}

func (f *recoveryFakeBackend) Restart(context.Context, *stack.Stack) (io.ReadCloser, error) {
	f.restarts++
	return io.NopCloser(strings.NewReader("$ fake restart\n")), nil
}

func (f *recoveryFakeBackend) Down(context.Context, *stack.Stack) (io.ReadCloser, error) {
	f.downs++
	return io.NopCloser(strings.NewReader("$ fake down\n")), nil
}

func newRecoveryTestServer(t *testing.T) (*server, *recoveryFakeBackend) {
	t.Helper()
	root := t.TempDir()
	stacksDir := root + "/stacks"
	mgr := stack.NewManager(stacksDir)
	st := &stack.Stack{
		Name:    "radarr",
		Compose: "services:\n  radarr:\n    image: example/radarr:latest\n",
	}
	if err := mgr.Save(st); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpdateState("radarr", func(s *stack.State) *stack.State {
		return &stack.State{Engine: "podman", DesiredState: "running"}
	}); err != nil {
		t.Fatal(err)
	}
	be := &recoveryFakeBackend{
		status: engine.StackStatus{
			State:      "running",
			Containers: []engine.ContainerStatus{{Name: "radarr_1", ID: "c1", Service: "radarr", State: "running"}},
		},
	}
	hm := newHealthMonitor()
	hm.run = func(fn func()) { fn() } // synchronous in tests
	srv := &server{
		manager:   mgr,
		backends:  map[string]engine.Backend{"podman": be},
		defEngine: "podman",
		fjordRoot: root,
		stacksDir: stacksDir,
		events:    newEventHub(),
		live:      newLiveStatus(),
		health:    hm,
	}
	return srv, be
}

func TestObserveContainersDetectsRestartJumps(t *testing.T) {
	hm := newHealthMonitor()
	now := time.Now()

	st := engine.StackStatus{
		State:      "running",
		Containers: []engine.ContainerStatus{{Name: "app_1", ID: "id1", State: "running", Restarts: 0}},
	}
	hm.observeContainers("app", &st, now)
	if st.Containers[0].Detail != "" {
		t.Fatalf("initial baseline should have no detail, got %q", st.Containers[0].Detail)
	}

	st.Containers[0].Restarts = 3
	hm.observeContainers("app", &st, now.Add(30*time.Second))
	if !strings.Contains(st.Containers[0].Detail, "crash-looping: restarted 3 times") {
		t.Fatalf("expected crash-looping detail after restart jump, got %q", st.Containers[0].Detail)
	}
}

func TestHealthRecoveryNoneRecordsAndClearsFailure(t *testing.T) {
	srv, _ := newRecoveryTestServer(t)
	now := time.Now()

	full, _ := srv.manager.Get("radarr")
	crashed := engine.StackStatus{
		State: "partial",
		Containers: []engine.ContainerStatus{{
			Name: "radarr_1", Service: "radarr", State: "crashed",
			Detail: "the app keeps crashing: database locked",
		}},
	}
	srv.observeStackHealth(full, crashed, now)

	state, err := srv.manager.LoadState("radarr")
	if err != nil || state == nil || state.LastFailure == nil {
		t.Fatalf("expected LastFailure recorded, got %+v (err=%v)", state, err)
	}
	if state.LastFailure.Action != "health" || !strings.Contains(state.LastFailure.Message, "database locked") {
		t.Fatalf("unexpected LastFailure: %+v", state.LastFailure)
	}
	if len(state.RecoveryEvents) != 1 || state.RecoveryEvents[0].Outcome != "detected" {
		t.Fatalf("unexpected RecoveryEvents: %+v", state.RecoveryEvents)
	}

	// Once the stack is running cleanly again, the health failure clears.
	full, _ = srv.manager.Get("radarr")
	healthy := engine.StackStatus{
		State:      "running",
		Containers: []engine.ContainerStatus{{Name: "radarr_1", Service: "radarr", State: "running"}},
	}
	srv.observeStackHealth(full, healthy, now.Add(time.Minute))

	state, _ = srv.manager.LoadState("radarr")
	if state.LastFailure != nil {
		t.Fatalf("expected health LastFailure cleared when running again, got %+v", state.LastFailure)
	}
}

func TestHealthRecoveryRestartCapAndCooldown(t *testing.T) {
	srv, be := newRecoveryTestServer(t)
	now := time.Now()

	_ = srv.manager.UpdateState("radarr", func(st *stack.State) *stack.State {
		st.Recovery = &stack.RecoveryPolicy{Action: stack.RecoveryRestart, MaxTries: 2, Cooldown: 60}
		return st
	})

	crashed := engine.StackStatus{
		State: "partial",
		Containers: []engine.ContainerStatus{{
			Name: "radarr_1", Service: "radarr", State: "crashed",
			Detail: "the app keeps crashing: panic",
		}},
	}

	// Attempt 1 runs immediately.
	full, _ := srv.manager.Get("radarr")
	srv.observeStackHealth(full, crashed, now)
	if be.restarts != 1 {
		t.Fatalf("restarts = %d, want 1", be.restarts)
	}

	// Within cooldown (30s < 60s): skipped.
	full, _ = srv.manager.Get("radarr")
	srv.observeStackHealth(full, crashed, now.Add(30*time.Second))
	if be.restarts != 1 {
		t.Fatalf("restarts during cooldown = %d, want 1", be.restarts)
	}

	// Past cooldown (65s): Attempt 2 runs.
	full, _ = srv.manager.Get("radarr")
	srv.observeStackHealth(full, crashed, now.Add(65*time.Second))
	if be.restarts != 2 {
		t.Fatalf("restarts after cooldown = %d, want 2", be.restarts)
	}

	// Still crashed at 130s: cap (2) reached -> exhausted, no 3rd restart.
	full, _ = srv.manager.Get("radarr")
	srv.observeStackHealth(full, crashed, now.Add(130*time.Second))
	if be.restarts != 2 {
		t.Fatalf("restarts after cap = %d, want 2", be.restarts)
	}
	state, _ := srv.manager.LoadState("radarr")
	if state.LastFailure == nil || !strings.Contains(state.LastFailure.Message, "gave up after 2 restarts") {
		t.Fatalf("expected exhausted failure message, got %+v", state.LastFailure)
	}
	if len(state.RecoveryEvents) != 3 || state.RecoveryEvents[2].Outcome != "exhausted" {
		t.Fatalf("expected 3 events ending in exhausted, got %+v", state.RecoveryEvents)
	}
}

func TestHealthRecoveryStopBringsStackDown(t *testing.T) {
	srv, be := newRecoveryTestServer(t)
	now := time.Now()

	_ = srv.manager.UpdateState("radarr", func(st *stack.State) *stack.State {
		st.Recovery = &stack.RecoveryPolicy{Action: stack.RecoveryStop}
		return st
	})

	crashed := engine.StackStatus{
		State: "partial",
		Containers: []engine.ContainerStatus{{
			Name: "radarr_1", Service: "radarr", State: "crashed",
			Detail: "the app keeps crashing",
		}},
	}
	full, _ := srv.manager.Get("radarr")
	srv.observeStackHealth(full, crashed, now)

	if be.downs != 1 {
		t.Fatalf("downs = %d, want 1", be.downs)
	}
	state, _ := srv.manager.LoadState("radarr")
	if state.DesiredState != "stopped" {
		t.Fatalf("DesiredState = %q, want stopped", state.DesiredState)
	}
	if state.LastFailure == nil || !strings.Contains(state.LastFailure.Message, "stopped by recovery policy") {
		t.Fatalf("unexpected LastFailure: %+v", state.LastFailure)
	}
}

func TestStackRecoveryPolicyEndpoint(t *testing.T) {
	srv, _ := newRecoveryTestServer(t)
	mux := http.NewServeMux()
	srv.routes(mux)

	body := bytes.NewBufferString(`{"action":"restart","maxTries":4,"cooldown":120}`)
	req := httptest.NewRequest(http.MethodPost, "/api/stacks/radarr/recovery-policy", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST recovery-policy status = %d: %s", rec.Code, rec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/stacks/radarr", nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET stack status = %d", getRec.Code)
	}
	var got stackWithStatus
	if err := json.NewDecoder(getRec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Recovery.Action != "restart" || got.Recovery.MaxTries != 4 || got.Recovery.Cooldown != 120 {
		t.Fatalf("unexpected recovery view: %+v", got.Recovery)
	}
}
