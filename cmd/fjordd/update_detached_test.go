package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/registry"
	"github.com/daemonless/fjord/pkg/stack"
	"github.com/daemonless/fjord/pkg/updates"
)

// updatingEngine records the stack each Update is handed, and can hold the
// update open or fail it.
type updatingEngine struct {
	engine.Backend
	caps engine.Capabilities
	gate chan struct{} // Update waits on it when set
	fail error
	mu   sync.Mutex
	seen []*stack.Stack
	svcs [][]string
}

func (e *updatingEngine) Capabilities() engine.Capabilities { return e.caps }
func (e *updatingEngine) RunningImages(context.Context, *stack.Stack) ([]engine.RunningImage, error) {
	return nil, nil
}
func (e *updatingEngine) Update(_ context.Context, st *stack.Stack, services []string) (io.ReadCloser, error) {
	if e.gate != nil {
		<-e.gate
	}
	if e.fail != nil {
		return nil, e.fail
	}
	e.mu.Lock()
	e.seen = append(e.seen, st)
	e.svcs = append(e.svcs, services)
	e.mu.Unlock()
	return io.NopCloser(strings.NewReader("pulled\nrecreated\n")), nil
}

func (e *updatingEngine) updates() []*stack.Stack {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*stack.Stack{}, e.seen...)
}

func changeVersionFixture(t *testing.T, name string, be *updatingEngine) *server {
	t.Helper()
	s := &server{manager: stack.NewManager(t.TempDir()), fjordRoot: t.TempDir(),
		backends: map[string]engine.Backend{"podman": be}}
	s.platformsFn = func(context.Context, string, string) ([]string, error) { return []string{registry.HostPlatform()}, nil }
	if err := s.manager.Save(&stack.Stack{Name: name, Compose: "services:\n  web:\n    image: repo/web:1.0\n"}); err != nil {
		t.Fatal(err)
	}
	s.manager.SetEngine(name, "podman")
	return s
}

func postSetTag(s *server, name, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.stackSetTag(w, httptest.NewRequest(http.MethodPost, "/api/stacks/"+name+"/set-tag", bytes.NewBufferString(body)), name)
	return w
}

func waitIdle(t *testing.T, name string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if loadBusy(name) == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s still busy", name)
}

// A version change with update is one job of the daemon's: set-tag answers
// once the compose names the new tag with the update already under way,
// busy and with output for any page, and the engine is handed the compose
// that names the new tag. Nothing of it waits on the page.
func TestSetTagWithUpdateIsOneDaemonJob(t *testing.T) {
	be := &updatingEngine{Backend: engine.Unavailable("podman"), gate: make(chan struct{})}
	s := changeVersionFixture(t, "cv-one", be)
	w := postSetTag(s, "cv-one", `{"tag":"2.0","update":true}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"updating":true`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if op := loadBusy("cv-one"); op == nil || op.action != "update" {
		t.Fatalf("the stack should be busy updating, is %+v", op)
	}
	if v, ok := outputs.get("cv-one"); !ok || v.Done || v.Action != "update" {
		t.Errorf("a page asking now should see an update running: %+v", v)
	}
	if st, _ := s.manager.Get("cv-one"); !strings.Contains(st.Compose, "repo/web:2.0") {
		t.Errorf("the compose should name the new tag before the update runs:\n%s", st.Compose)
	}
	close(be.gate)
	waitIdle(t, "cv-one")
	seen := be.updates()
	if len(seen) != 1 || !strings.Contains(seen[0].Compose, "repo/web:2.0") {
		t.Fatalf("the engine should have updated from the retagged compose once, saw %d", len(seen))
	}
	if v, _ := outputs.get("cv-one"); !v.Done || !strings.Contains(v.Text, "recreated") {
		t.Errorf("the update's output should be kept for a page opened later: %+v", v)
	}
	if state, _ := s.manager.LoadState("cv-one"); state.LastFailure != nil {
		t.Errorf("a clean update must not leave a failure: %+v", state.LastFailure)
	}
}

// The part of the stack to update is named and checked before anything is
// written; a busy stack is refused before the compose is touched. Both
// used to be found out after the retag, with the compose already changed.
func TestSetTagWithUpdateRefusesBeforeWriting(t *testing.T) {
	be := &updatingEngine{Backend: engine.Unavailable("podman")}
	s := changeVersionFixture(t, "cv-busy", be)
	if w := postSetTag(s, "cv-busy", `{"tag":"2.0","update":true,"update_services":["db"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("a service the stack does not have: %d %s", w.Code, w.Body.String())
	}
	unlock, ok := lockStackAs("cv-busy", "up")
	if !ok {
		t.Fatal("lock")
	}
	w := postSetTag(s, "cv-busy", `{"tag":"2.0","update":true}`)
	unlock()
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "starting") {
		t.Errorf("busy stack: %d %s", w.Code, w.Body.String())
	}
	if st, _ := s.manager.Get("cv-busy"); !strings.Contains(st.Compose, "repo/web:1.0") {
		t.Errorf("a refused change must leave the compose alone:\n%s", st.Compose)
	}
	if len(be.updates()) != 0 {
		t.Error("nothing should have been updated")
	}
	// Without update, set-tag is what it was: the ref written, nothing run.
	if w := postSetTag(s, "cv-busy", `{"tag":"3.0"}`); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "updating") {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
	if op := loadBusy("cv-busy"); op != nil {
		t.Errorf("no update was asked for, but the stack is busy %+v", op)
	}
}

// An update that fails with no page there is still a recorded failure on
// the stack, with the reason in its output.
func TestSetTagWithUpdateRecordsAFailure(t *testing.T) {
	be := &updatingEngine{Backend: engine.Unavailable("podman"), fail: errors.New("pull: manifest unknown")}
	s := changeVersionFixture(t, "cv-fail", be)
	if w := postSetTag(s, "cv-fail", `{"tag":"2.0","update":true}`); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	waitIdle(t, "cv-fail")
	state, _ := s.manager.LoadState("cv-fail")
	if state.LastFailure == nil || state.LastFailure.Action != "update" || !strings.Contains(state.LastFailure.Message, "manifest unknown") {
		t.Errorf("the failure should be on the stack: %+v", state.LastFailure)
	}
	if v, _ := outputs.get("cv-fail"); !v.Done || !strings.Contains(v.Text, "[error] pull: manifest unknown") {
		t.Errorf("output: %+v", v)
	}
}

// POST /api/updates updates every stack whose fleet check says "available",
// one after another in the daemon, narrows to the behind services when the
// engine supports per-service updates, skips current/upgrade/busy stacks, and
// forgets each stack from the fleet cache as it finishes.
func TestUpdateAllUpdatesAvailableStacksInOrder(t *testing.T) {
	be := &updatingEngine{
		Backend: engine.Unavailable("podman"),
		caps:    engine.Capabilities{UpdateServices: true},
		gate:    make(chan struct{}),
	}
	s := &server{
		manager:   stack.NewManager(t.TempDir()),
		fjordRoot: t.TempDir(),
		backends:  map[string]engine.Backend{"podman": be},
	}
	for _, name := range []string{"ua-one", "ua-two", "ua-cur", "ua-upg", "ua-busy"} {
		compose := "services:\n  web:\n    image: repo/web:latest\n  db:\n    image: repo/db:16\n"
		if err := s.manager.Save(&stack.Stack{Name: name, Compose: compose}); err != nil {
			t.Fatal(err)
		}
		s.manager.SetEngine(name, "podman")
	}
	s.fleet.put("ua-one", updates.Status{
		State: "available",
		Services: []updates.ServiceStatus{
			{Service: "web", State: "available"},
			{Service: "db", State: "current"},
		},
	})
	s.fleet.put("ua-two", updates.Status{
		State: "available",
		Services: []updates.ServiceStatus{
			{Service: "web", State: "available"},
			{Service: "db", State: "available"},
		},
	})
	s.fleet.put("ua-cur", updates.Status{State: "current"})
	s.fleet.put("ua-upg", updates.Status{State: "upgrade", NewTag: "2.0", ToVersion: "2.0"})
	s.fleet.put("ua-busy", updates.Status{State: "available"})

	holdBusy, ok := lockStackAs("ua-busy", "up")
	if !ok {
		t.Fatal("lock ua-busy")
	}
	defer holdBusy()

	w := httptest.NewRecorder()
	s.handleUpdates(w, httptest.NewRequest(http.MethodPost, "/api/updates", strings.NewReader(`{}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Updating []string `json:"updating"`
		Skipped  []string `json:"skipped"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resp.Updating, []string{"ua-one", "ua-two"}) {
		t.Fatalf("updating = %v, want [ua-one ua-two]", resp.Updating)
	}
	if !reflect.DeepEqual(resp.Skipped, []string{"ua-busy"}) {
		t.Fatalf("skipped = %v, want [ua-busy]", resp.Skipped)
	}
	if op := loadBusy("ua-one"); op == nil || op.action != "update" {
		t.Errorf("ua-one busy = %+v, want update", op)
	}
	if op := loadBusy("ua-two"); op == nil || op.action != "update" {
		t.Errorf("ua-two busy = %+v, want update", op)
	}

	close(be.gate)
	waitIdle(t, "ua-one")
	waitIdle(t, "ua-two")

	seen := be.updates()
	if len(seen) != 2 || seen[0].Name != "ua-one" || seen[1].Name != "ua-two" {
		t.Fatalf("updated stacks = %+v, want ua-one then ua-two", seen)
	}
	be.mu.Lock()
	gotSvcs := append([][]string{}, be.svcs...)
	be.mu.Unlock()
	if !reflect.DeepEqual(gotSvcs, [][]string{{"web"}, {"web", "db"}}) {
		t.Errorf("updated services = %v, want [[web] [web db]]", gotSvcs)
	}
	if _, ok := s.fleet.results["ua-one"]; ok {
		t.Error("ua-one should be forgotten from fleet cache after update")
	}
	if _, ok := s.fleet.results["ua-two"]; ok {
		t.Error("ua-two should be forgotten from fleet cache after update")
	}
}

// When retagForUpdate fails during a batch update, the daemon records the
// failure and skips running Update on the old tag.
func TestUpdateAllRecordsRetagFailureInsteadOfRunningOldTag(t *testing.T) {
	be := &updatingEngine{Backend: engine.Unavailable("podman")}
	s := &server{
		manager:   stack.NewManager(t.TempDir()),
		fjordRoot: t.TempDir(),
		backends:  map[string]engine.Backend{"podman": be},
	}
	// Invalid YAML in compose causes retagForUpdate to fail.
	if err := s.manager.Save(&stack.Stack{Name: "ua-bad", Compose: "services: [\n"}); err != nil {
		t.Fatal(err)
	}
	s.manager.SetEngine("ua-bad", "podman")
	s.fleet.put("ua-bad", updates.Status{State: "upgrade", NewTag: "2.0"})

	w := httptest.NewRecorder()
	s.handleUpdates(w, httptest.NewRequest(http.MethodPost, "/api/updates", strings.NewReader(`{"stacks":["ua-bad"]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	waitIdle(t, "ua-bad")
	if len(be.updates()) != 0 {
		t.Errorf("expected Update to be skipped after retag failure, got %d calls", len(be.updates()))
	}
	state, _ := s.manager.LoadState("ua-bad")
	if state == nil || state.LastFailure == nil || !strings.Contains(state.LastFailure.Message, "retag for update failed") {
		t.Errorf("expected recorded retag failure, got %+v", state)
	}
}
