package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/history"
	"github.com/daemonless/fjord/pkg/stack"
)

func TestFailureNoticesLifecycleAndHistoryDedup(t *testing.T) {
	stubRPHooks(t)
	root := t.TempDir()
	s := &server{
		manager:   stack.NewManager(root + "/stacks"),
		fjordRoot: root,
		hist:      history.New(root),
		rpSched:   newRPScheduleState(root),
	}
	_ = s.manager.Save(&stack.Stack{Name: "radarr", Compose: "services:\n  radarr:\n    image: x\n"})
	_ = s.manager.UpdateState("radarr", func(*stack.State) *stack.State {
		return &stack.State{
			SchemaVersion:      1,
			DisplayName:        "Radarr",
			DesiredState:       "running",
			Dataset:            "zroot/apps/radarr",
			RestorePointPolicy: "daily",
		}
	})

	failErr := errors.New("pool full")
	snapshotScheduledFn = func(context.Context, string, time.Time, ...string) (string, error) {
		return "", failErr
	}
	pruneAutomaticSnapshotsFn = func(context.Context, string, time.Duration, time.Time) (int, error) {
		return 0, nil
	}

	day1 := time.Date(2026, 11, 21, 2, 0, 0, 0, time.Local)
	day2 := day1.AddDate(0, 0, 1)
	day3 := day1.AddDate(0, 0, 2)
	day4 := day1.AddDate(0, 0, 3)
	day5 := day1.AddDate(0, 0, 4)
	day6 := day1.AddDate(0, 0, 5)

	// 1. Three failures make one notice with count 3 and only 1 failed History entry.
	s.runDailyRestorePointsPass(history.ByAutomatic, day1)
	s.runDailyRestorePointsPass(history.ByAutomatic, day2)
	s.runDailyRestorePointsPass(history.ByAutomatic, day3)

	st, err := s.manager.LoadState("radarr")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.FailureNotices) != 1 {
		t.Fatalf("notices = %+v, want 1", st.FailureNotices)
	}
	n := st.FailureNotices[0]
	if n.Kind != stack.NoticeDailyRestorePoint || n.Count != 3 || n.Dismissed {
		t.Fatalf("after 3 failures: got %+v, want count=3 dismissed=false", n)
	}
	if !n.FirstAt.Equal(day1.UTC()) || !n.LastAt.Equal(day3.UTC()) {
		t.Fatalf("firstAt=%v lastAt=%v, want %v..%v", n.FirstAt, n.LastAt, day1.UTC(), day3.UTC())
	}
	entries := s.hist.Stack("radarr", 20)
	if len(entries) != 1 || entries[0].Result != history.Failed {
		t.Fatalf("history after 3 nights = %+v, want exactly 1 failed entry", entries)
	}

	// 2. Dismiss hides it, stays hidden on the next failure while count increments to 4,
	// and GET /api/notices still counts the dismissed notice.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/stacks/radarr/notices/dismiss", strings.NewReader(`{"kind":"daily-restore-point"}`))
	s.handleStackRoutes(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("dismiss status = %d, body = %s", rec.Code, rec.Body.String())
	}

	failErr = errors.New("still full")
	s.runDailyRestorePointsPass(history.ByAutomatic, day4)
	st, _ = s.manager.LoadState("radarr")
	if len(st.FailureNotices) != 1 || st.FailureNotices[0].Count != 4 || !st.FailureNotices[0].Dismissed || st.FailureNotices[0].Message != "still full" {
		t.Fatalf("after dismiss + 4th failure: got %+v", st.FailureNotices)
	}
	if got := len(s.hist.Stack("radarr", 20)); got != 1 {
		t.Fatalf("history after 4th failure = %d entries, want still 1", got)
	}

	nRec := httptest.NewRecorder()
	s.handleNotices(nRec, httptest.NewRequest(http.MethodGet, "/api/notices", nil))
	if nRec.Code != http.StatusOK {
		t.Fatalf("GET /api/notices = %d", nRec.Code)
	}
	var payload struct {
		Notices []fleetNotice `json:"notices"`
	}
	if err := json.Unmarshal(nRec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Notices) != 1 || !payload.Notices[0].Dismissed || payload.Notices[0].Count != 4 || payload.Notices[0].DisplayName != "Radarr" {
		t.Fatalf("GET /api/notices = %+v, want dismissed notice with count=4", payload.Notices)
	}

	// 3. Success clears the notice and writes 1 recovery History entry.
	snapshotScheduledFn = func(_ context.Context, ds string, at time.Time, _ ...string) (string, error) {
		return ds + "@fjord-scheduled-ok", nil
	}
	s.runDailyRestorePointsPass(history.ByAutomatic, day5)
	st, _ = s.manager.LoadState("radarr")
	if len(st.FailureNotices) != 0 {
		t.Fatalf("after recovery: notices = %+v, want empty", st.FailureNotices)
	}
	entries = s.hist.Stack("radarr", 20)
	if len(entries) != 2 || entries[0].Result != history.OK {
		t.Fatalf("history after recovery = %+v, want 2 entries (newest OK)", entries)
	}

	// 4. Failure after recovery opens a brand-new notice (count 1, dismissed=false) and adds 1 failed entry.
	snapshotScheduledFn = func(context.Context, string, time.Time, ...string) (string, error) {
		return "", errors.New("dataset busy")
	}
	s.runDailyRestorePointsPass(history.ByAutomatic, day6)
	st, _ = s.manager.LoadState("radarr")
	if len(st.FailureNotices) != 1 || st.FailureNotices[0].Count != 1 || st.FailureNotices[0].Dismissed {
		t.Fatalf("after post-recovery failure: notices = %+v, want count=1 dismissed=false", st.FailureNotices)
	}
	if got := len(s.hist.Stack("radarr", 20)); got != 3 {
		t.Fatalf("history after post-recovery failure = %d, want 3", got)
	}
}

func TestDismissNoticeValidation(t *testing.T) {
	root := t.TempDir()
	s := &server{
		manager:   stack.NewManager(root + "/stacks"),
		fjordRoot: root,
	}
	_ = s.manager.Save(&stack.Stack{Name: "app", Compose: "services:\n  web:\n    image: x\n"})

	badKind := httptest.NewRecorder()
	s.handleStackRoutes(badKind, httptest.NewRequest(http.MethodPost, "/api/stacks/app/notices/dismiss", strings.NewReader(`{"kind":"bogus"}`)))
	if badKind.Code != http.StatusBadRequest {
		t.Fatalf("bad kind status = %d, want 400", badKind.Code)
	}

	missing := httptest.NewRecorder()
	s.handleStackRoutes(missing, httptest.NewRequest(http.MethodPost, "/api/stacks/nope/notices/dismiss", strings.NewReader(`{"kind":"auto-update"}`)))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing stack status = %d, want 404", missing.Code)
	}
}

func TestAutoUpdateNoSnapshotNoticeDedupAndClear(t *testing.T) {
	root := t.TempDir()
	be := &stubUpdateBackend{Backend: engine.Unavailable("podman")}
	s := &server{
		manager:   stack.NewManager(root + "/stacks"),
		fjordRoot: root,
		hist:      history.New(root),
		auto:      newAutoState(root),
		rpSched:   newRPScheduleState(root),
		backends:  map[string]engine.Backend{"podman": be},
		defEngine: "podman",
	}
	_ = s.manager.Save(&stack.Stack{Name: "app", Compose: "services:\n  web:\n    image: x\n"})
	_ = s.manager.UpdateState("app", func(*stack.State) *stack.State {
		return &stack.State{SchemaVersion: 1, DesiredState: "running", Engine: "podman", Dataset: "zroot/apps/app", RestorePointPolicy: "updates"}
	})

	oldSnap := snapshotBeforeUpdateFn
	oldPrune := pruneAutomaticSnapshotsFn
	t.Cleanup(func() {
		snapshotBeforeUpdateFn = oldSnap
		pruneAutomaticSnapshotsFn = oldPrune
	})
	snapshotBeforeUpdateFn = func(context.Context, string, time.Time, int, ...string) (string, error) {
		return "", errors.New("out of space")
	}
	pruneAutomaticSnapshotsFn = func(context.Context, string, time.Duration, time.Time) (int, error) { return 0, nil }

	act := autoAction{Stack: "app", Services: []string{"web"}}
	// Two consecutive automatic update runs aborted by snapshot failure -> count=2, only 1 failed History entry.
	s.autoUpdateStack(act)
	s.autoUpdateStack(act)

	st, _ := s.manager.LoadState("app")
	if len(st.FailureNotices) != 1 || st.FailureNotices[0].Kind != stack.NoticeAutoUpdateNoSnapshot || st.FailureNotices[0].Count != 2 {
		t.Fatalf("notices = %+v, want auto-update-no-snapshot count=2", st.FailureNotices)
	}
	if entries := s.hist.Stack("app", 20); len(entries) != 1 || entries[0].Result != history.Failed {
		t.Fatalf("history = %+v, want 1 failed entry", entries)
	}

	// Next automatic update succeeds -> clears the notice.
	snapshotBeforeUpdateFn = func(_ context.Context, ds string, _ time.Time, _ int, _ ...string) (string, error) {
		return ds + "@fjord-update-ok", nil
	}
	res := s.autoUpdateStack(act)
	if res.Result != "updated" {
		t.Fatalf("expected updated, got %+v", res)
	}
	st, _ = s.manager.LoadState("app")
	if len(st.FailureNotices) != 0 {
		t.Fatalf("notices after success = %+v, want empty", st.FailureNotices)
	}
}
