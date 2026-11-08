package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	statusFn func(context.Context, *stack.Stack) (engine.StackStatus, error)
	restarts int
	downs    int
	downOut  string
	downErr  error
}

func (f *recoveryFakeBackend) Capabilities() engine.Capabilities {
	return engine.Capabilities{}
}

func (f *recoveryFakeBackend) Status(ctx context.Context, st *stack.Stack) (engine.StackStatus, error) {
	if f.statusFn != nil {
		return f.statusFn(ctx, st)
	}
	return f.status, nil
}

func (f *recoveryFakeBackend) Restart(context.Context, *stack.Stack) (io.ReadCloser, error) {
	f.restarts++
	return io.NopCloser(strings.NewReader("$ fake restart\n")), nil
}

func (f *recoveryFakeBackend) Down(context.Context, *stack.Stack) (io.ReadCloser, error) {
	f.downs++
	if f.downErr != nil {
		return nil, f.downErr
	}
	if f.downOut != "" {
		return io.NopCloser(strings.NewReader(f.downOut)), nil
	}
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
	be.status = crashed

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
	be.status = crashed
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

func TestHealthRecoveryIgnoresCleanOneShotExit(t *testing.T) {
	srv, _ := newRecoveryTestServer(t)
	now := time.Now()

	st, err := srv.manager.Get("radarr")
	if err != nil {
		t.Fatal(err)
	}
	st.Compose = "services:\n  migrate:\n    image: example/migrate:latest\n    restart: \"no\"\n  radarr:\n    image: example/radarr:latest\n    restart: unless-stopped\n"
	if err := srv.manager.Save(st); err != nil {
		t.Fatal(err)
	}

	withExitedOneShot := engine.StackStatus{
		State: "partial",
		Containers: []engine.ContainerStatus{
			{Name: "radarr_migrate_1", Service: "migrate", State: "exited", ExitCode: 0},
			{Name: "radarr_radarr_1", Service: "radarr", State: "running"},
		},
	}

	for i := range 3 {
		full, _ := srv.manager.Get("radarr")
		srv.observeStackHealth(full, withExitedOneShot, now.Add(time.Duration(i)*30*time.Second))
	}
	state, _ := srv.manager.LoadState("radarr")
	if state.LastFailure != nil || len(state.RecoveryEvents) != 0 {
		t.Fatalf("clean one-shot exit should not trigger failure: LastFailure=%+v, Events=%+v", state.LastFailure, state.RecoveryEvents)
	}

	// Non-zero exit on a one-shot container still flags failure after two checks.
	failedOneShot := engine.StackStatus{
		State: "partial",
		Containers: []engine.ContainerStatus{
			{Name: "radarr_migrate_1", Service: "migrate", State: "exited", ExitCode: 1, Detail: "exited with code 1 -- see Logs"},
			{Name: "radarr_radarr_1", Service: "radarr", State: "running"},
		},
	}
	full, _ := srv.manager.Get("radarr")
	srv.observeStackHealth(full, failedOneShot, now.Add(2*time.Minute))
	full, _ = srv.manager.Get("radarr")
	srv.observeStackHealth(full, failedOneShot, now.Add(3*time.Minute))

	state, _ = srv.manager.LoadState("radarr")
	if state.LastFailure == nil || !strings.Contains(state.LastFailure.Message, "migrate: exited with code 1") {
		t.Fatalf("expected non-zero one-shot exit to record failure, got %+v", state.LastFailure)
	}

	// Once migrate succeeds on a retry (exited 0) while radarr is running, health failure clears.
	full, _ = srv.manager.Get("radarr")
	srv.observeStackHealth(full, withExitedOneShot, now.Add(4*time.Minute))
	state, _ = srv.manager.LoadState("radarr")
	if state.LastFailure != nil {
		t.Fatalf("expected LastFailure cleared once only clean one-shot is exited, got %+v", state.LastFailure)
	}

	// An appjail-style "stopped" container or a podman "created" container on a
	// restart: "no" service never actually ran to a clean exit and must still
	// be flagged as unhealthy.
	for _, badState := range []string{"stopped", "created"} {
		neverRan := engine.StackStatus{
			State: "partial",
			Containers: []engine.ContainerStatus{
				{Name: "radarr_migrate_1", Service: "migrate", State: badState, ExitCode: 0},
				{Name: "radarr_radarr_1", Service: "radarr", State: "running"},
			},
		}
		if why, _ := unhealthyReason(st, neverRan); why == "" {
			t.Fatalf("expected %q container on restart: no service to be flagged unhealthy", badState)
		}
	}

	// A stack where every service has restart: "no" (stack-wide "Don't restart"
	// policy) is not a one-shot sidecar; if its app exits 0 while DesiredState
	// is running, it must still be reported as stopped unexpectedly.
	allNoStack := &stack.Stack{
		Name:    "radarr",
		Compose: "services:\n  radarr:\n    image: example/radarr:latest\n    restart: \"no\"\n",
	}
	allNoExited := engine.StackStatus{
		State: "stopped",
		Containers: []engine.ContainerStatus{
			{Name: "radarr_1", Service: "radarr", State: "exited", ExitCode: 0},
		},
	}
	if why, _ := unhealthyReason(allNoStack, allNoExited); why == "" {
		t.Fatal("expected whole stack with restart: no that exited 0 to be flagged unhealthy")
	}
}

func TestHealthRecoverySkipsStoppedBusyOrHealthyStack(t *testing.T) {
	srv, be := newRecoveryTestServer(t)
	now := time.Now()

	_ = srv.manager.UpdateState("radarr", func(st *stack.State) *stack.State {
		st.Recovery = &stack.RecoveryPolicy{Action: stack.RecoveryRestart, MaxTries: 3, Cooldown: 10}
		return st
	})

	crashed := engine.StackStatus{
		State: "partial",
		Containers: []engine.ContainerStatus{{
			Name: "radarr_1", Service: "radarr", State: "crashed",
			Detail: "the app keeps crashing",
		}},
	}
	be.status = crashed

	// 1. User stops the stack between observeStackHealth and recoverRestart acquiring the lock.
	var queued func()
	srv.health.run = func(fn func()) { queued = fn }
	full, _ := srv.manager.Get("radarr")
	srv.observeStackHealth(full, crashed, now)
	if queued == nil {
		t.Fatal("expected recovery action queued")
	}
	_ = srv.manager.SetDesiredState("radarr", "stopped")
	queued()
	if be.restarts != 0 {
		t.Fatalf("restarts on stopped stack = %d, want 0", be.restarts)
	}

	// Restore DesiredState to running and run synchronously for the remaining checks.
	_ = srv.manager.SetDesiredState("radarr", "running")
	srv.health.reset("radarr")
	srv.health.run = func(fn func()) { fn() }

	// 2. Stack has a stay-up watch active: recovery must NOT take it over or stop the watch.
	unlock, ok := lockStackAs("radarr", "update")
	if !ok {
		t.Fatal("lockStackAs failed")
	}
	watchStopped := false
	wctx := watchable(context.Background(), "radarr")
	engine.WatchStarted(wctx, func() { watchStopped = true })

	srv.recoverRestart("radarr", "the app keeps crashing", 1, 3, now.Add(time.Minute), time.Time{})
	srv.recoverStop("radarr", "the app keeps crashing", now.Add(time.Minute), time.Time{})
	unlock()

	if watchStopped {
		t.Fatal("recovery took over and stopped an active user watch")
	}
	if be.restarts != 0 || be.downs != 0 {
		t.Fatalf("recovery ran on busy stack: restarts=%d, downs=%d", be.restarts, be.downs)
	}

	// 3. Stack recovered on its own before recoverRestart checked status under the lock.
	be.status = engine.StackStatus{
		State:      "running",
		Containers: []engine.ContainerStatus{{Name: "radarr_1", Service: "radarr", State: "running"}},
	}
	srv.recoverRestart("radarr", "the app keeps crashing", 1, 3, now.Add(2*time.Minute), time.Time{})
	if be.restarts != 0 {
		t.Fatalf("restarts on already-healthy stack = %d, want 0", be.restarts)
	}
}

func TestHealthRecoveryStopFailureKeepsDesiredRunning(t *testing.T) {
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
	be.status = crashed
	be.downErr = errors.New("appjail-director.yml missing")

	full, _ := srv.manager.Get("radarr")
	srv.observeStackHealth(full, crashed, now)

	state, _ := srv.manager.LoadState("radarr")
	if state.DesiredState != "running" {
		t.Fatalf("DesiredState after failed Down = %q, want running", state.DesiredState)
	}
	if state.LastFailure == nil || !strings.Contains(state.LastFailure.Message, "recovery stop failed: appjail-director.yml missing") {
		t.Fatalf("unexpected LastFailure after Down error: %+v", state.LastFailure)
	}
	if len(state.RecoveryEvents) != 1 || state.RecoveryEvents[0].Outcome != "failed" {
		t.Fatalf("expected failed recovery event, got %+v", state.RecoveryEvents)
	}

	// Also verify when Down returns nil error but emits an [error] line in the output stream.
	srv.health.reset("radarr")
	be.downErr = nil
	be.downOut = "$ podman-compose down\n[error] container stop timed out\n"

	full, _ = srv.manager.Get("radarr")
	srv.observeStackHealth(full, crashed, now.Add(time.Minute))

	state, _ = srv.manager.LoadState("radarr")
	if state.DesiredState != "running" {
		t.Fatalf("DesiredState after streamed [error] = %q, want running", state.DesiredState)
	}
	if state.LastFailure == nil || !strings.Contains(state.LastFailure.Message, "recovery stop failed: container stop timed out") {
		t.Fatalf("unexpected LastFailure after streamed [error]: %+v", state.LastFailure)
	}
	if len(state.RecoveryEvents) != 2 || state.RecoveryEvents[1].Outcome != "failed" {
		t.Fatalf("expected 2nd failed recovery event, got %+v", state.RecoveryEvents)
	}
}

func TestHealthRecoveryNoneDeduplicatesCrashLoopCounts(t *testing.T) {
	srv, _ := newRecoveryTestServer(t)
	now := time.Now()

	details := []string{
		"crash-looping: restarted 2 times",
		"crash-looping: restarted 3 times",
		"crash-looping: restarted 7 times",
	}
	for i, d := range details {
		full, _ := srv.manager.Get("radarr")
		status := engine.StackStatus{
			State: "running",
			Containers: []engine.ContainerStatus{{
				Name: "radarr_1", Service: "radarr", State: "running",
				Restarts: i + 2, Detail: d,
			}},
		}
		srv.observeStackHealth(full, status, now.Add(time.Duration(i)*30*time.Second))
	}

	state, _ := srv.manager.LoadState("radarr")
	if len(state.RecoveryEvents) != 1 {
		t.Fatalf("expected 1 deduplicated event across increasing restart counts, got %d: %+v",
			len(state.RecoveryEvents), state.RecoveryEvents)
	}

	// Also verify "(N restarts)" and "after N restarts" forms deduplicate.
	srv.health.reset("radarr")
	for i, d := range []string{
		"exited with code 1 after 1 restart -- see Logs",
		"exited with code 1 after 4 restarts -- see Logs",
	} {
		full, _ := srv.manager.Get("radarr")
		status := engine.StackStatus{
			State: "partial",
			Containers: []engine.ContainerStatus{{
				Name: "radarr_1", Service: "radarr", State: "crashed",
				Restarts: i + 1, Detail: d,
			}},
		}
		srv.observeStackHealth(full, status, now.Add(time.Duration(i+10)*30*time.Second))
	}
	state, _ = srv.manager.LoadState("radarr")
	if len(state.RecoveryEvents) != 2 {
		t.Fatalf("expected 1 new event for second episode, got %d total: %+v",
			len(state.RecoveryEvents), state.RecoveryEvents)
	}
}

func TestHealthRecoveryReasonWithErrorBracketDoesNotFailStopOrRestart(t *testing.T) {
	srv, be := newRecoveryTestServer(t)
	now := time.Now()

	// R-1: a crash reason containing "[error]" (such as nginx's log line) is
	// formatted into the recovery header; errorWatch must not treat the header
	// itself as an engine failure.
	crashed := engine.StackStatus{
		State: "partial",
		Containers: []engine.ContainerStatus{{
			Name: "radarr_1", Service: "radarr", State: "crashed",
			Detail: "2026/11/08 [error] 12#0: bind() failed",
		}},
	}
	be.status = crashed

	_ = srv.manager.UpdateState("radarr", func(st *stack.State) *stack.State {
		st.Recovery = &stack.RecoveryPolicy{Action: stack.RecoveryStop}
		return st
	})
	full, _ := srv.manager.Get("radarr")
	srv.observeStackHealth(full, crashed, now)

	state, _ := srv.manager.LoadState("radarr")
	if state.DesiredState != "stopped" {
		t.Fatalf("DesiredState = %q, want stopped when Down succeeds despite [error] in reason", state.DesiredState)
	}
	if len(state.RecoveryEvents) != 1 || state.RecoveryEvents[0].Outcome != "stopped" {
		t.Fatalf("expected stopped outcome, got %+v", state.RecoveryEvents)
	}

	// Same check for recoverRestart.
	_ = srv.manager.UpdateState("radarr", func(st *stack.State) *stack.State {
		st.DesiredState = "running"
		st.Recovery = &stack.RecoveryPolicy{Action: stack.RecoveryRestart, MaxTries: 3, Cooldown: 10}
		st.RecoveryEvents = nil
		return st
	})
	srv.health.reset("radarr")
	full, _ = srv.manager.Get("radarr")
	srv.observeStackHealth(full, crashed, now.Add(time.Minute))

	state, _ = srv.manager.LoadState("radarr")
	if len(state.RecoveryEvents) != 1 || state.RecoveryEvents[0].Outcome != "restarted" {
		t.Fatalf("expected restarted outcome despite [error] in reason, got %+v", state.RecoveryEvents)
	}
}

func TestHealthRecoveryMarksBusyAndBoundsStatusUnderLock(t *testing.T) {
	srv, be := newRecoveryTestServer(t)
	now := time.Now()

	// R-2: while lockStackForRecovery re-checks Status under the stack lock,
	// loadBusy must report the stack busy, and the Status context must carry a
	// short deadline (statusWait) rather than the 10-minute action timeout.
	var sawBusy *busyOp
	var hasShortDeadline bool
	be.statusFn = func(ctx context.Context, _ *stack.Stack) (engine.StackStatus, error) {
		sawBusy = loadBusy("radarr")
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= statusWait+time.Second {
			hasShortDeadline = true
		}
		return be.status, nil
	}
	be.status = engine.StackStatus{
		State: "partial",
		Containers: []engine.ContainerStatus{{
			Name: "radarr_1", Service: "radarr", State: "crashed",
			Detail: "the app keeps crashing",
		}},
	}

	srv.recoverRestart("radarr", "the app keeps crashing", 1, 3, now, time.Time{})
	if sawBusy == nil || sawBusy.action != "restart" {
		t.Fatalf("expected stack marked busy with restart during under-lock Status check, got %+v", sawBusy)
	}
	if !hasShortDeadline {
		t.Fatal("expected under-lock Status check context bounded by statusWait")
	}
	if loadBusy("radarr") != nil {
		t.Fatal("expected busy cleared after recoverRestart")
	}
}
