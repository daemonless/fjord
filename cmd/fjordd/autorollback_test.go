package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
	"github.com/daemonless/fjord/pkg/updates"
)

// recreatingEngine recreates what it is asked to and says so; fail makes
// the recreate's health watch fail.
type recreatingEngine struct {
	engine.Backend
	recreated *[]string
	fail      bool
}

func (e recreatingEngine) Capabilities() engine.Capabilities {
	return engine.Capabilities{PinServices: true, UpdateServices: true}
}
func (e recreatingEngine) Recreate(_ context.Context, _ *stack.Stack, svcs []string) (io.ReadCloser, error) {
	*e.recreated = append(*e.recreated, svcs...)
	if e.fail {
		return io.NopCloser(strings.NewReader("[error] web exited\n")), nil
	}
	return io.NopCloser(strings.NewReader("ok\n")), nil
}

func rollbackFixture(t *testing.T, compose string, fail bool) (*server, *[]string) {
	t.Helper()
	s := unpinFixture(t, compose)
	var got []string
	s.backends = map[string]engine.Backend{"podman": recreatingEngine{engine.Unavailable("podman"), &got, fail}}
	s.manager.SetEngine("app", "podman")
	s.fleet = fleetUpdates{}
	return s, &got
}

// A rebuild on the same tag that failed: the service is held on the digest
// it ran (the tag left as written would pull the bad build back), recreated,
// and the compose otherwise as it was.
func TestAutoRollbackHoldsTheDigest(t *testing.T) {
	s, got := rollbackFixture(t, "services:\n  web:\n    image: repo/web:latest\n", false)
	state := &stack.State{SchemaVersion: 1, Engine: "podman",
		Rollback: map[string]stack.RollbackImage{"web": {Compose: "repo/web:latest", Ref: "repo/web:latest", Digest: "sha256:good"}}}
	s.manager.UpdateState("app", func(*stack.State) *stack.State { return state })
	act := autoAction{Stack: "app", Services: []string{"web"}, Keys: map[string]string{"web": "sha256:bad"}}
	back, why := s.autoRollback(act, "services:\n  web:\n    image: repo/web:latest\n", "TAG=2.0\n", state)
	st, _ := s.manager.Get("app")
	if !back || !strings.Contains(st.Compose, "repo/web:latest@sha256:good") || strings.Join(*got, ",") != "web" {
		t.Fatalf("back=%v %q compose:\n%s recreated %v", back, why, st.Compose, *got)
	}
}

// A version bump that failed: the compose and the .env go back as they were
// before the retag, which is the old version, with no pin.
func TestAutoRollbackUndoesTheRetag(t *testing.T) {
	s, got := rollbackFixture(t, "services:\n  web:\n    image: repo/web:${TAG}\n", false)
	s.manager.Update("app", func(st *stack.Stack) error { st.Env = "TAG=2.1\n"; return nil }) // the retag
	state := &stack.State{SchemaVersion: 1, Engine: "podman"}
	act := autoAction{Stack: "app", Services: []string{"web"}, Retags: []autoRetag{{"web", "2.1"}}}
	back, why := s.autoRollback(act, "services:\n  web:\n    image: repo/web:${TAG}\n", "TAG=2.0\n", state)
	st, _ := s.manager.Get("app")
	if !back || st.Env != "TAG=2.0\n" || strings.Contains(st.Compose, "@") || len(*got) != 1 {
		t.Fatalf("back=%v %q env %q compose:\n%s", back, why, st.Env, st.Compose)
	}
}

// A rollback whose own recreate fails says so, once.
func TestAutoRollbackThatFails(t *testing.T) {
	s, got := rollbackFixture(t, "services:\n  web:\n    image: repo/web:${TAG}\n", true)
	state := &stack.State{SchemaVersion: 1, Engine: "podman"}
	s.manager.UpdateState("app", func(*stack.State) *stack.State { return state })
	act := autoAction{Stack: "app", Services: []string{"web"}, Retags: []autoRetag{{"web", "2.1"}}}
	back, why := s.autoRollback(act, "services:\n  web:\n    image: repo/web:${TAG}\n", "TAG=2.0\n", state)
	if back || !strings.Contains(why, "web exited") || len(*got) != 1 {
		t.Fatalf("back=%v %q recreated %v", back, why, *got)
	}
}

// The update that failed here waits; a different one goes.
func TestPlanAutoHeld(t *testing.T) {
	now := time.Now()
	st := autoStack{Name: "app", PerService: true, Services: []autoService{
		{Name: "web", Policy: updates.Minors, Cand: updates.Candidate{Class: updates.Rebuild}, Key: "sha256:bad", Held: "2026-11-23"},
	}}
	if a := planAuto(st, now); a.Result != "waiting" || !strings.Contains(a.Reason, "failed on 2026-11-23 and was rolled back") {
		t.Fatalf("held: %+v", a)
	}
	st.Services[0].Held, st.Services[0].Key, st.Services[0].Unhold = "", "sha256:newer", "repo/web:latest"
	a := planAuto(st, now)
	if len(a.Services) != 1 || a.Unholds["web"] != "repo/web:latest" || a.Keys["web"] != "sha256:newer" {
		t.Fatalf("newer: %+v", a)
	}
}

// Two automatic failures within a day pause them all; one a day apart does not.
func TestAutoBreaker(t *testing.T) {
	a := &autoState{path: t.TempDir() + "/autoupdate.json"}
	now := time.Now()
	if a.failed(now.Add(-25 * time.Hour)) {
		t.Fatal("paused on the first")
	}
	if a.failed(now) {
		t.Fatal("paused by two a day apart")
	}
	if !a.failed(now.Add(time.Hour)) || a.f.PausedAt.IsZero() {
		t.Fatal("two within a day did not pause")
	}
}
