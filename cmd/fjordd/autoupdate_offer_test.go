package main

import (
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

func TestAutoUpdateDefault(t *testing.T) {
	root := t.TempDir()
	s := &server{manager: stack.NewManager(root + "/stacks"), fjordRoot: root, auto: newAutoState(root)}

	get := func() (string, string, bool) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.handleAutoUpdate(rec, httptest.NewRequest(http.MethodGet, "/api/autoupdate", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/autoupdate: %d %s", rec.Code, rec.Body)
		}
		var out struct {
			Start         string `json:"start"`
			End           string `json:"end"`
			DefaultForNew bool   `json:"defaultForNew"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Start, out.End, out.DefaultForNew
	}

	if _, _, def := get(); def {
		t.Fatal("defaultForNew should be false by default")
	}

	post := func(body string) int {
		rec := httptest.NewRecorder()
		s.handleAutoUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/autoupdate", strings.NewReader(body)))
		return rec.Code
	}
	if code := post(`{"defaultForNew":true}`); code != http.StatusNoContent {
		t.Fatalf("POST defaultForNew true: %d", code)
	}
	if s0, e0, def := get(); !def || s0 != "03:00" || e0 != "05:00" {
		t.Fatalf("after enable: %s-%s default=%v", s0, e0, def)
	}
	if code := post(`{"start":"01:30","end":"02:45"}`); code != http.StatusNoContent {
		t.Fatalf("POST window: %d", code)
	}
	if s0, e0, def := get(); !def || s0 != "01:30" || e0 != "02:45" {
		t.Fatalf("window update must keep defaultForNew: %s-%s default=%v", s0, e0, def)
	}
	if code := post(`{"defaultForNew":false}`); code != http.StatusNoContent {
		t.Fatalf("POST defaultForNew false: %d", code)
	}
	if s0, e0, def := get(); def || s0 != "01:30" || e0 != "02:45" {
		t.Fatalf("after disable: %s-%s default=%v", s0, e0, def)
	}
}

// The offer goes to stacks no one has answered for: one turned off on
// purpose is an answer. Answered once, it is not made again.
func TestAutoUpdateOffer(t *testing.T) {
	root := t.TempDir()
	s := &server{manager: stack.NewManager(root + "/stacks"), fjordRoot: root}
	for name, policy := range map[string]string{"old": "", "chose-off": "off", "new": "minor"} {
		if err := s.manager.Save(&stack.Stack{Name: name, Compose: "services:\n  web:\n    image: x\n"}); err != nil {
			t.Fatal(err)
		}
		p := policy
		s.manager.UpdateState(name, func(*stack.State) *stack.State {
			return &stack.State{SchemaVersion: 1, DesiredState: "running", UpdatePolicy: p}
		})
	}
	if got := strings.Join(s.unasked(), ","); got != "old" {
		t.Fatalf("offered to %q", got)
	}
	rec := httptest.NewRecorder()
	s.autoUpdateOffer(rec, httptest.NewRequest(http.MethodPost, "/api/autoupdate/offer", strings.NewReader(`{"answer":"all"}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"old"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for name, want := range map[string]string{"old": "minor", "chose-off": "off", "new": "minor"} {
		if st, _ := s.manager.LoadState(name); st.UpdatePolicy != want {
			t.Errorf("%s: %q, want %q", name, st.UpdatePolicy, want)
		}
	}
	if len(s.unasked()) != 0 {
		t.Fatal("offered again")
	}
}

func TestAutoUpdateAll(t *testing.T) {
	root := t.TempDir()
	s := &server{manager: stack.NewManager(root + "/stacks"), fjordRoot: root, auto: newAutoState(root)}
	for name, policy := range map[string]string{"a": "", "b": "off", "c": "minor"} {
		if err := s.manager.Save(&stack.Stack{Name: name, Compose: "services:\n  web:\n    image: x\n"}); err != nil {
			t.Fatal(err)
		}
		p := policy
		s.manager.UpdateState(name, func(*stack.State) *stack.State {
			return &stack.State{SchemaVersion: 1, DesiredState: "running", UpdatePolicy: p}
		})
	}
	if on, total := s.autoUpdateCounts(); on != 1 || total != 3 {
		t.Fatalf("initial counts: %d/%d, want 1/3", on, total)
	}

	rec := httptest.NewRecorder()
	s.handleAutoUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/autoupdate/all", strings.NewReader(`{"on":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /all true: %d %s", rec.Code, rec.Body)
	}
	if on, total := s.autoUpdateCounts(); on != 3 || total != 3 {
		t.Fatalf("after all on: %d/%d, want 3/3", on, total)
	}
	if len(s.unasked()) != 0 {
		t.Fatal("offer should be marked answered after /all")
	}

	rec = httptest.NewRecorder()
	s.handleAutoUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/autoupdate/all", strings.NewReader(`{"on":false}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /all false: %d %s", rec.Code, rec.Body)
	}
	if on, total := s.autoUpdateCounts(); on != 0 || total != 3 {
		t.Fatalf("after all off: %d/%d, want 0/3", on, total)
	}
	for _, name := range []string{"a", "b", "c"} {
		if st, _ := s.manager.LoadState(name); st.UpdatePolicy != "off" {
			t.Errorf("%s: %q, want off", name, st.UpdatePolicy)
		}
	}

	// Bulk toggle preserves per-service overrides.
	if err := s.manager.SetUpdatePolicy("a", "minor", map[string]string{"db": "rebuilds"}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	s.handleAutoUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/autoupdate/all", strings.NewReader(`{"on":false}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /all false (preserve services): %d", rec.Code)
	}
	if st, _ := s.manager.LoadState("a"); st.ServicePolicy["db"] != "rebuilds" {
		t.Fatalf("bulk off wiped ServicePolicy: %+v", st.ServicePolicy)
	}
}

type stubUpdateBackend struct {
	engine.Backend
	updateCalls int
}

func (b *stubUpdateBackend) Name() string { return "podman" }
func (b *stubUpdateBackend) Update(_ context.Context, _ *stack.Stack, _ []string) (io.ReadCloser, error) {
	b.updateCalls++
	return io.NopCloser(strings.NewReader("updated ok\n")), nil
}
func (b *stubUpdateBackend) RunningImages(_ context.Context, _ *stack.Stack) ([]engine.RunningImage, error) {
	return []engine.RunningImage{{Service: "web", Ref: "ghcr.io/example/app:2.0", Digest: "sha256:new"}}, nil
}

func TestAutoUpdateSnapshotGuard(t *testing.T) {
	root := t.TempDir()
	be := &stubUpdateBackend{Backend: engine.Unavailable("podman")}
	s := &server{
		manager:   stack.NewManager(root + "/stacks"),
		fjordRoot: root,
		auto:      newAutoState(root),
		backends:  map[string]engine.Backend{"podman": be},
		defEngine: "podman",
	}
	origCompose := "services:\n  web:\n    image: ghcr.io/example/app:2.0\n"
	if err := s.manager.Save(&stack.Stack{Name: "app", Compose: origCompose}); err != nil {
		t.Fatal(err)
	}
	prevRB := map[string]stack.RollbackImage{
		"web": {Ref: "ghcr.io/example/app:1.0", Digest: "sha256:old"},
	}
	if err := s.manager.UpdateState("app", func(*stack.State) *stack.State {
		return &stack.State{
			SchemaVersion: 1,
			DesiredState:  "running",
			Engine:        "podman",
			Dataset:       "zroot/apps/app",
			Rollback:      prevRB,
		}
	}); err != nil {
		t.Fatal(err)
	}

	oldSnap := snapshotBeforeUpdateFn
	snapshotBeforeUpdateFn = func(context.Context, string, time.Time, int, ...string) (string, error) {
		return "", errors.New("out of space")
	}
	t.Cleanup(func() { snapshotBeforeUpdateFn = oldSnap })

	// 1. Automatic update aborts before touching running containers or Rollback record,
	// and puts retagged compose back.
	act := autoAction{
		Stack:    "app",
		Services: []string{"web"},
		Retags:   []autoRetag{{service: "web", tag: "2.1"}},
		Keys:     map[string]string{"web": "2.1"},
	}
	res := s.autoUpdateStack(act)
	if res.Result != "failed" || !strings.Contains(res.Reason, "out of space") {
		t.Fatalf("autoUpdateStack result = %+v, want failed out of space", res)
	}
	if be.updateCalls != 0 {
		t.Fatalf("backend Update called %d times on snapshot failure, want 0", be.updateCalls)
	}
	st, err := s.manager.Get("app")
	if err != nil {
		t.Fatal(err)
	}
	if st.Compose != origCompose {
		t.Fatalf("compose not restored after snapshot abort:\n%s", st.Compose)
	}
	if st.State == nil || st.State.Rollback["web"].Digest != prevRB["web"].Digest {
		t.Fatalf("existing rollback record was clobbered: %+v", st.State.Rollback)
	}
	if st.State.AutoRollback != nil || len(st.State.AutoHold) > 0 {
		t.Fatalf("snapshot abort must not run autoRollback or set AutoHold: %+v %+v", st.State.AutoRollback, st.State.AutoHold)
	}

	// 2. Manual update warns and proceeds when snapshot fails.
	stream, err := s.updateStream(context.Background(), st, s.pending.now(), []string{"web"}, true)
	if err != nil {
		t.Fatalf("manual updateStream failed on snapshot error: %v", err)
	}
	out, _ := io.ReadAll(stream)
	stream.Close()
	if be.updateCalls != 1 {
		t.Fatalf("manual update backend calls = %d, want 1", be.updateCalls)
	}
	if !strings.Contains(string(out), "no snapshot of its data before this update: out of space") {
		t.Fatalf("manual update missing snapshot warning: %q", string(out))
	}
}
