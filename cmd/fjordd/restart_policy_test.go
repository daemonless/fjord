package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

func TestBootStart(t *testing.T) {
	us := map[string]string{"app": "unless-stopped", "db": "always"}
	always := map[string]string{"app": "always"}
	onFail := map[string]string{"app": "on-failure:5"}
	never := map[string]string{"app": "no", "db": "no"}
	oneShot := map[string]string{"app": "unless-stopped", "migrate": "no"}
	unset := map[string]string{"app": ""}
	cases := []struct {
		name     string
		desired  string
		restarts map[string]string
		boot     bool
		want     bool
	}{
		{"running unless-stopped, boot", "running", us, true, true},
		{"running unless-stopped, fjordd restart", "running", us, false, true},
		{"running on-failure", "running", onFail, true, true},
		{"running never, boot", "running", never, true, false},
		{"running never, fjordd restart", "running", never, false, false},
		{"running with a one-shot", "running", oneShot, true, true},
		{"running, no policy written (older stacks)", "running", unset, true, true},
		{"running, engine without policies", "running", nil, true, true},
		{"stopped always, boot", "stopped", always, true, true},
		{"stopped always, fjordd restart", "stopped", always, false, false},
		{"stopped mixed with always, boot", "stopped", us, true, true},
		{"stopped unless-stopped only, boot", "stopped", map[string]string{"app": "unless-stopped"}, true, false},
		{"stopped on-failure, boot", "stopped", onFail, true, false},
		{"stopped, engine without policies", "stopped", nil, true, false},
		{"deleting", "deleting", always, true, false},
	}
	for _, c := range cases {
		if got := bootStart(c.desired, c.restarts, c.boot); got != c.want {
			t.Errorf("%s: bootStart = %v, want %v", c.name, got, c.want)
		}
	}
}

// The first fjordd start after a boot finds no marker and leaves one; a
// fjordd restart finds it.
func TestHostBooted(t *testing.T) {
	old := bootMarker
	bootMarker = filepath.Join(t.TempDir(), "fjordd.booted")
	defer func() { bootMarker = old }()
	if !hostBooted() {
		t.Fatal("first start after boot not seen as a boot")
	}
	if hostBooted() {
		t.Fatal("a fjordd restart seen as a boot")
	}
}

type restartEngine struct {
	engine.Backend
	caps engine.Capabilities
}

func (e restartEngine) Capabilities() engine.Capabilities { return e.caps }

var (
	onExit   = engine.Capabilities{RestartPolicies: []string{"unless-stopped", "always", "on-failure", "no"}, RestartOnExit: true}
	bootOnly = engine.Capabilities{RestartPolicies: []string{"unless-stopped", "always", "no"}}
	noPolicy = engine.Capabilities{}
)

func restartFixture(t *testing.T, caps engine.Capabilities) *server {
	t.Helper()
	s := &server{
		manager:  stack.NewManager(t.TempDir()),
		backends: map[string]engine.Backend{"podman": restartEngine{engine.Unavailable("podman"), caps}},
	}
	st := &stack.Stack{Name: "gitea", Compose: "services:\n  gitea:\n    image: g\n    restart: unless-stopped\n  postgres:\n    image: p\n    restart: always\n"}
	if err := s.manager.Save(st); err != nil {
		t.Fatal(err)
	}
	if err := s.manager.SetEngine("gitea", "podman"); err != nil {
		t.Fatal(err)
	}
	if err := s.manager.EnsureState("gitea"); err != nil {
		t.Fatal(err)
	}
	return s
}

func postRestart(s *server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.stackRestart(rec, httptest.NewRequest(http.MethodPost, "/api/stacks/gitea/restart-policy", strings.NewReader(body)), "gitea")
	return rec
}

// Setting a policy writes every service and leaves both for Apply to
// recreate.
func TestStackRestartWritesEveryService(t *testing.T) {
	s := restartFixture(t, onExit)
	if rec := postRestart(s, `{"policy":"on-failure","retries":5}`); rec.Code != http.StatusOK {
		t.Fatalf("restart-policy: %d %s", rec.Code, rec.Body)
	}
	st, _ := s.manager.Get("gitea")
	got := composepkg.RestartOf(st.Compose)
	if got.Mixed || got.Policy != "on-failure" || got.Retries != 5 {
		t.Fatalf("policy now %+v\n%s", got, st.Compose)
	}
	state, _ := s.manager.LoadState("gitea")
	if state == nil || strings.Join(state.PendingServices, ",") != "gitea,postgres" {
		t.Fatalf("pending = %+v", state)
	}
}

func TestStackRestartRefuses(t *testing.T) {
	s := restartFixture(t, onExit)
	for _, body := range []string{`{"policy":"sometimes"}`, `{"policy":""}`, `not json`} {
		if rec := postRestart(s, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, rec.Code)
		}
	}
	// An engine that does not apply it is told so, not quietly written.
	s = restartFixture(t, noPolicy)
	if rec := postRestart(s, `{"policy":"always"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported engine: %d", rec.Code)
	}
	st, _ := s.manager.Get("gitea")
	if !composepkg.RestartOf(st.Compose).Mixed {
		t.Fatal("the compose was changed anyway")
	}
}

// Retries mean nothing beside a policy other than on-failure.
func TestStackRestartDropsRetries(t *testing.T) {
	s := restartFixture(t, onExit)
	if rec := postRestart(s, `{"policy":"always","retries":3}`); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	st, _ := s.manager.Get("gitea")
	if r := composepkg.ServiceRestarts(st.Compose); r["gitea"] != "always" {
		t.Fatalf("restarts = %v", r)
	}
}

// On an engine that only starts it at boot, the policy is kept in the state
// and the compose is left alone; it offers no on-failure.
func TestStackRestartBootOnly(t *testing.T) {
	s := restartFixture(t, bootOnly)
	if rec := postRestart(s, `{"policy":"no"}`); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	st, _ := s.manager.Get("gitea")
	if st.State == nil || st.State.Restart != "no" {
		t.Fatalf("state = %+v", st.State)
	}
	if !composepkg.RestartOf(st.Compose).Mixed {
		t.Fatal("the compose was changed")
	}
	if v := s.restartViewOf(st); v.Policy != "no" || v.OnExit || len(v.Policies) != 3 {
		t.Fatalf("view = %+v", v)
	}
	if got := s.bootRestarts(st); got[""] != "no" || len(got) != 1 {
		t.Fatalf("boot weighs %v", got)
	}
	if rec := postRestart(s, `{"policy":"on-failure","retries":5}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("on-failure on a boot-only engine: %d", rec.Code)
	}
}

// Before any policy is set, a boot-only stack keeps the old rule.
func TestBootRestartsUnset(t *testing.T) {
	s := restartFixture(t, bootOnly)
	st, _ := s.manager.Get("gitea")
	if got := s.bootRestarts(st); got != nil {
		t.Fatalf("boot weighs %v, want nothing", got)
	}
	s = restartFixture(t, onExit)
	st, _ = s.manager.Get("gitea")
	if got := s.bootRestarts(st); got["postgres"] != "always" {
		t.Fatalf("boot weighs %v, want the compose's", got)
	}
}

func TestDefaultRestartFor(t *testing.T) {
	saved := savedSettings{DefaultRestartFor: map[string]string{"podman": "on-failure:3", "appjail": "on-failure:3", "other": "sometimes"}}
	for _, c := range []struct {
		eng  string
		caps engine.Capabilities
		want string
	}{
		{"podman", onExit, "on-failure:3"},
		{"appjail", bootOnly, "unless-stopped"}, // not offered there
		{"other", onExit, "unless-stopped"},
		{"unset", onExit, "unless-stopped"},
	} {
		if got := defaultRestartFor(saved, c.eng, c.caps).String(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.eng, got, c.want)
		}
	}
}

// At a host boot a stack its policy leaves down (Don't restart) is recorded
// as stopped: it is not a stack that failed to come up.
func TestStartOnBootLeftDownIsStopped(t *testing.T) {
	s := restartFixture(t, onExit)
	if rec := postRestart(s, `{"policy":"no"}`); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if err := s.manager.SetDesiredState("gitea", "running"); err != nil {
		t.Fatal(err)
	}
	old := bootMarker
	bootMarker = filepath.Join(t.TempDir(), "fjordd.booted") // absent: a host boot
	defer func() { bootMarker = old }()
	startOnBoot(s)
	if st, _ := s.manager.LoadState("gitea"); st == nil || st.DesiredState != "stopped" {
		t.Fatalf("desired = %+v, want stopped", st)
	}
}

// During a stay-up watch the stack reads as idle and the page offers the
// restart chip; the change used to get a 409 (the plain stack lock) while
// Start, Stop and Update end the watch. It ends the watch too now.
func TestRestartPolicyTakesOverAWatch(t *testing.T) {
	s := restartFixture(t, onExit)
	stopped := holdWatching(t, "gitea")
	if rec := postRestart(s, `{"policy":"no"}`); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	select {
	case <-stopped:
	default:
		t.Error("the watch was not ended")
	}
}
