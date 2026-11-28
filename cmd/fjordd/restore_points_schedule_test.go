package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/dataset"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/history"
	"github.com/daemonless/fjord/pkg/stack"
)

func stubRPHooks(t *testing.T) {
	t.Helper()
	oldSnap := snapshotScheduledFn
	oldPrune := pruneAutomaticSnapshotsFn
	oldWritten := writtenSinceFn
	oldList := listSnapshotsFn
	oldExists := snapshotExistsFn
	writtenSinceFn = func(context.Context, string, string) (int64, error) { return 4096, nil }
	listSnapshotsFn = func(context.Context, string, bool) ([]dataset.SnapInfo, error) { return nil, nil }
	snapshotExistsFn = func(context.Context, string) bool { return false }
	t.Cleanup(func() {
		snapshotScheduledFn = oldSnap
		pruneAutomaticSnapshotsFn = oldPrune
		writtenSinceFn = oldWritten
		listSnapshotsFn = oldList
		snapshotExistsFn = oldExists
	})
}

func TestRestorePointSettingsAndPolicy(t *testing.T) {
	root := t.TempDir()
	s := &server{
		manager:   stack.NewManager(root + "/stacks"),
		fjordRoot: root,
		rpSched:   newRPScheduleState(root),
	}
	if err := s.manager.Save(&stack.Stack{Name: "app", Compose: "services:\n  web:\n    image: x\n"}); err != nil {
		t.Fatal(err)
	}
	if err := s.manager.UpdateState("app", func(*stack.State) *stack.State {
		return &stack.State{SchemaVersion: 1, DesiredState: "running", Dataset: "zroot/apps/app"}
	}); err != nil {
		t.Fatal(err)
	}

	// Defaults: updates, 30 days, 02:00.
	rec := httptest.NewRecorder()
	s.handleRestorePointSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings/restore-points", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	var got struct {
		DefaultPolicy string `json:"defaultPolicy"`
		RetentionDays int    `json:"retentionDays"`
		DailyTime     string `json:"dailyTime"`
		ReadyCount    int    `json:"readyCount"`
		DailyCount    int    `json:"dailyCount"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.DefaultPolicy != "updates" || got.RetentionDays != 30 || got.DailyTime != "02:00" || got.ReadyCount != 1 || got.DailyCount != 0 {
		t.Fatalf("defaults: %+v", got)
	}

	// Update host settings.
	rec = httptest.NewRecorder()
	s.handleRestorePointSettings(rec, httptest.NewRequest(http.MethodPost, "/api/settings/restore-points",
		strings.NewReader(`{"defaultPolicy":"daily","retentionDays":14,"dailyTime":"03:30"}`)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST settings: %d %s", rec.Code, rec.Body)
	}
	def, days, hm := restorePointSettingsOf(loadSettings(root))
	if def != "daily" || days != 14 || hm != "03:30" {
		t.Fatalf("persisted: %s %d %s", def, days, hm)
	}

	// Per-app policy -> daily.
	rec = httptest.NewRecorder()
	s.stackRestorePointPolicy(rec, httptest.NewRequest(http.MethodPost, "/api/stacks/app/restore-points/policy",
		strings.NewReader(`{"policy":"daily"}`)), "app")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST policy: %d %s", rec.Code, rec.Body)
	}
	st, _ := s.manager.LoadState("app")
	if effectiveRestorePointPolicy(st) != "daily" {
		t.Fatalf("effective policy: %q", effectiveRestorePointPolicy(st))
	}
	if ready, daily, total := s.restorePointCounts(); ready != 1 || daily != 1 || total != 1 {
		t.Fatalf("counts: %d/%d/%d", ready, daily, total)
	}

	// Invalid retention rejected.
	rec = httptest.NewRecorder()
	s.handleRestorePointSettings(rec, httptest.NewRequest(http.MethodPost, "/api/settings/restore-points",
		strings.NewReader(`{"retentionDays":45}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid retention, got %d", rec.Code)
	}
}

func TestNextDailyTime(t *testing.T) {
	loc := time.Local
	before := time.Date(2026, 11, 27, 1, 30, 0, 0, loc)
	if got := nextDailyTime(before, "02:00", false); got.Day() != 27 || got.Hour() != 2 {
		t.Fatalf("before: %v", got)
	}
	after := time.Date(2026, 11, 27, 2, 30, 0, 0, loc)
	if got := nextDailyTime(after, "02:00", false); got.Day() != 28 || got.Hour() != 2 {
		t.Fatalf("after: %v", got)
	}
	if got := nextDailyTime(before, "02:00", true); got.Day() != 28 || got.Hour() != 2 {
		t.Fatalf("ranToday: %v", got)
	}
}

func TestRunDailyRestorePointsPassAndBusyRetry(t *testing.T) {
	stubRPHooks(t)
	root := t.TempDir()
	s := &server{
		manager:   stack.NewManager(root + "/stacks"),
		fjordRoot: root,
		hist:      history.New(root),
		rpSched:   newRPScheduleState(root),
	}
	days := 14
	if err := updateSettings(root, func(st *savedSettings) {
		st.RestorePointRetentionDays = &days
		st.RestorePointDailyTime = "02:00"
	}); err != nil {
		t.Fatal(err)
	}

	seed := func(name, ds, policy string) {
		t.Helper()
		if err := s.manager.Save(&stack.Stack{Name: name, Compose: "services:\n  web:\n    image: x\n"}); err != nil {
			t.Fatal(err)
		}
		if err := s.manager.UpdateState(name, func(*stack.State) *stack.State {
			return &stack.State{
				SchemaVersion:      1,
				DesiredState:       "running",
				Dataset:            ds,
				RestorePointPolicy: policy,
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed("daily-ok", "zroot/apps/daily-ok", "daily")
	seed("daily-busy", "zroot/apps/daily-busy", "daily")
	seed("daily-fail", "zroot/apps/daily-fail", "daily")
	seed("updates-only", "zroot/apps/updates-only", "updates")
	seed("off-stack", "zroot/apps/off-stack", "off")
	seed("plain-folder", "", "daily")

	// Hold the stack lock on daily-busy during the initial pass.
	unlockBusy, ok := lockStackAs("daily-busy", "restore")
	if !ok {
		t.Fatal("could not lock daily-busy")
	}

	var snapped []string
	var pruned []string
	var prunedMaxAge time.Duration
	snapshotScheduledFn = func(_ context.Context, ds string, at time.Time, _ ...string) (string, error) {
		snapped = append(snapped, ds)
		if ds == "zroot/apps/daily-fail" {
			return "", errors.New("pool full")
		}
		return fmt.Sprintf("%s@%s%d", ds, dataset.ScheduledSnapPrefix, at.Unix()), nil
	}
	pruneAutomaticSnapshotsFn = func(_ context.Context, ds string, maxAge time.Duration, _ time.Time) (int, error) {
		pruned = append(pruned, ds)
		prunedMaxAge = maxAge
		return 0, nil
	}

	// Before dailyTime: must not run yet.
	before := time.Date(2026, 11, 27, 1, 59, 0, 0, time.Local)
	if s.shouldRunDailyRestorePoints(before) {
		t.Fatal("shouldRunDailyRestorePoints returned true before 02:00")
	}
	at := time.Date(2026, 11, 27, 2, 0, 0, 0, time.Local)
	if !s.shouldRunDailyRestorePoints(at) {
		t.Fatal("shouldRunDailyRestorePoints returned false at 02:00")
	}

	rec := s.runDailyRestorePointsPass(history.ByAutomatic, at)
	if rec.Saved != 1 || rec.Failed != 1 || rec.Day != "2026-11-27" || !slices.Equal(rec.Busy, []string{"daily-busy"}) {
		t.Fatalf("unexpected run record: %+v", rec)
	}
	slices.Sort(snapped)
	if !slices.Equal(snapped, []string{"zroot/apps/daily-fail", "zroot/apps/daily-ok"}) {
		t.Fatalf("snapped = %v", snapped)
	}
	if !slices.Equal(pruned, []string{"zroot/apps/daily-ok"}) || prunedMaxAge != 14*24*time.Hour {
		t.Fatalf("pruned = %v (maxAge=%v)", pruned, prunedMaxAge)
	}

	// Failure must record a failed History entry; success must not spam History.
	if entries := s.hist.Stack("daily-fail", 10); len(entries) != 1 || entries[0].Result != history.Failed || !strings.Contains(entries[0].Message, "pool full") {
		t.Fatalf("daily-fail history = %+v", entries)
	}
	if okEntries := s.hist.Stack("daily-ok", 10); len(okEntries) != 0 {
		t.Fatalf("daily-ok history should be empty on success, got %+v", okEntries)
	}

	// 5 minutes later: still within 10-minute retry spacing -> must NOT retry yet.
	if s.shouldRunDailyRestorePoints(at.Add(5 * time.Minute)) {
		t.Fatal("retried before 10m interval")
	}

	// Manual run while daily-busy is still locked reports busy AND does not mix
	// its counts into today's automatic retry totals.
	manualRec := s.runDailyRestorePointsPass(history.ByYou, at.Add(6*time.Minute))
	if !slices.Equal(manualRec.Busy, []string{"daily-busy"}) {
		t.Fatalf("manual run should report busy apps, got %+v", manualRec)
	}

	// Release daily-busy lock and advance 10 minutes -> retries ONLY daily-busy,
	// never re-snapshots daily-ok, and preserves automatic day totals (2 saved, 1 failed).
	unlockBusy()
	retryAt := at.Add(10 * time.Minute)
	if !s.shouldRunDailyRestorePoints(retryAt) {
		t.Fatal("expected retry to be due at +10m")
	}
	snapped = nil
	rec2 := s.runDailyRestorePointsPass(history.ByAutomatic, retryAt)
	if !slices.Equal(snapped, []string{"zroot/apps/daily-busy"}) {
		t.Fatalf("retry snapped = %v, want only daily-busy", snapped)
	}
	if rec2.Saved != 2 || rec2.Failed != 1 || len(rec2.Busy) != 0 {
		t.Fatalf("after retry record = %+v", rec2)
	}

	// Same local day after busy cleared: must not run again.
	if s.shouldRunDailyRestorePoints(time.Date(2026, 11, 27, 23, 30, 0, 0, time.Local)) {
		t.Fatal("shouldRunDailyRestorePoints returned true after all apps done")
	}
	// Next day at 02:00: eligible again.
	if !s.shouldRunDailyRestorePoints(time.Date(2026, 11, 28, 2, 0, 0, 0, time.Local)) {
		t.Fatal("shouldRunDailyRestorePoints returned false on next day")
	}

	// Persisted atomically to restorepoints.json and survives reload.
	reloaded := newRPScheduleState(root)
	if reloaded.last.Day != "2026-11-27" || reloaded.last.Saved != 2 || reloaded.last.Failed != 1 {
		t.Fatalf("reloaded state = %+v", reloaded.last)
	}
}

func TestDailyBusyAllDayBecomesOneFailure(t *testing.T) {
	stubRPHooks(t)
	root := t.TempDir()
	s := &server{
		manager:   stack.NewManager(root + "/stacks"),
		fjordRoot: root,
		hist:      history.New(root),
		rpSched:   newRPScheduleState(root),
	}
	if err := s.manager.Save(&stack.Stack{Name: "stuck", Compose: "services:\n  web:\n    image: x\n"}); err != nil {
		t.Fatal(err)
	}
	_ = s.manager.UpdateState("stuck", func(*stack.State) *stack.State {
		return &stack.State{SchemaVersion: 1, DesiredState: "running", Dataset: "zroot/apps/stuck", RestorePointPolicy: "daily"}
	})
	unlock, ok := lockStackAs("stuck", "restore")
	if !ok {
		t.Fatal("could not lock stuck")
	}
	defer unlock()

	day1 := time.Date(2026, 11, 27, 2, 0, 0, 0, time.Local)
	rec := s.runDailyRestorePointsPass(history.ByAutomatic, day1)
	if len(rec.Busy) != 1 || rec.Failed != 0 {
		t.Fatalf("day1 rec = %+v", rec)
	}
	// Roll past midnight into day2: shouldRunDailyRestorePoints finalizes day1's busy app.
	day2Early := time.Date(2026, 11, 28, 0, 5, 0, 0, time.Local)
	if s.shouldRunDailyRestorePoints(day2Early) {
		t.Fatal("should not run day2 pass at 00:05")
	}
	if s.rpSched.last.Failed != 1 || len(s.rpSched.last.Busy) != 0 || !strings.Contains(strings.Join(s.rpSched.last.Errors, ";"), "stuck: busy all day") {
		t.Fatalf("after day rollover last = %+v", s.rpSched.last)
	}
	entries := s.hist.Stack("stuck", 10)
	if len(entries) != 1 || entries[0].Result != history.Failed || !strings.Contains(entries[0].Message, "busy all day") {
		t.Fatalf("stuck history = %+v", entries)
	}
}

func TestDailySkipWhenWrittenZero(t *testing.T) {
	stubRPHooks(t)
	root := t.TempDir()
	s := &server{
		manager:   stack.NewManager(root + "/stacks"),
		fjordRoot: root,
		rpSched:   newRPScheduleState(root),
	}
	seed := func(name, ds string) {
		_ = s.manager.Save(&stack.Stack{Name: name, Compose: "services:\n  web:\n    image: x\n"})
		_ = s.manager.UpdateState(name, func(*stack.State) *stack.State {
			return &stack.State{SchemaVersion: 1, DesiredState: "running", Dataset: ds, RestorePointPolicy: "daily"}
		})
	}
	seed("unchanged", "zroot/apps/unchanged")
	seed("first-ever", "zroot/apps/first-ever")
	seed("changed", "zroot/apps/changed")
	seed("get-err", "zroot/apps/get-err")

	var checkedSnap []string
	writtenSinceFn = func(_ context.Context, ds, snap string) (int64, error) {
		checkedSnap = append(checkedSnap, snap)
		switch ds {
		case "zroot/apps/unchanged":
			return 0, nil
		case "zroot/apps/changed":
			return 8192, nil
		default:
			return 0, errors.New("zfs get failed")
		}
	}
	listSnapshotsFn = func(_ context.Context, ds string, _ bool) ([]dataset.SnapInfo, error) {
		if ds == "zroot/apps/first-ever" {
			return nil, nil
		}
		return []dataset.SnapInfo{
			{Name: ds + "@fjord-scheduled-1", Kind: "scheduled"},
			{Name: ds + "@fjord-scheduled-2", Kind: "scheduled"},
		}, nil
	}

	var snapped, pruned []string
	snapshotScheduledFn = func(_ context.Context, ds string, at time.Time, _ ...string) (string, error) {
		snapped = append(snapped, ds)
		return fmt.Sprintf("%s@%s%d", ds, dataset.ScheduledSnapPrefix, at.Unix()), nil
	}
	pruneAutomaticSnapshotsFn = func(_ context.Context, ds string, _ time.Duration, _ time.Time) (int, error) {
		pruned = append(pruned, ds)
		return 0, nil
	}

	rec := s.runDailyRestorePointsPass(history.ByAutomatic, time.Date(2026, 11, 27, 2, 0, 0, 0, time.Local))
	if rec.Skipped != 1 || rec.Saved != 3 || rec.Failed != 0 {
		t.Fatalf("rec = %+v, want skipped=1 saved=3 failed=0", rec)
	}
	if !slices.Contains(checkedSnap, "zroot/apps/unchanged@fjord-scheduled-2") {
		t.Fatalf("must check written@newest-fjord-snap, checked=%v", checkedSnap)
	}
	slices.Sort(snapped)
	wantSnapped := []string{"zroot/apps/changed", "zroot/apps/first-ever", "zroot/apps/get-err"}
	if !slices.Equal(snapped, wantSnapped) {
		t.Fatalf("snapped = %v, want %v", snapped, wantSnapped)
	}
	// Pruning still runs for the skipped app too.
	if !slices.Contains(pruned, "zroot/apps/unchanged") {
		t.Fatalf("pruned = %v, must include unchanged", pruned)
	}
}

func TestDailyScheduledNameIdempotentAndManualDistinct(t *testing.T) {
	stubRPHooks(t)
	root := t.TempDir()
	s := &server{
		manager:   stack.NewManager(root + "/stacks"),
		fjordRoot: root,
		rpSched:   newRPScheduleState(root),
	}
	_ = s.manager.Save(&stack.Stack{Name: "app", Compose: "services:\n  web:\n    image: x\n"})
	_ = s.manager.UpdateState("app", func(*stack.State) *stack.State {
		return &stack.State{SchemaVersion: 1, DesiredState: "running", Dataset: "zroot/apps/app", RestorePointPolicy: "daily"}
	})

	created := map[string]bool{}
	var snapNames []string
	snapshotExistsFn = func(_ context.Context, fullSnap string) bool { return created[fullSnap] }
	snapshotScheduledFn = func(_ context.Context, ds string, at time.Time, _ ...string) (string, error) {
		name := fmt.Sprintf("%s@%s%d", ds, dataset.ScheduledSnapPrefix, at.Unix())
		created[name] = true
		snapNames = append(snapNames, name)
		return name, nil
	}
	pruneAutomaticSnapshotsFn = func(context.Context, string, time.Duration, time.Time) (int, error) { return 0, nil }

	// Catch-up pass at 06:15 names the snapshot by 02:00 scheduled time.
	catchUp := time.Date(2026, 11, 27, 6, 15, 0, 0, time.Local)
	wantScheduled := fmt.Sprintf("zroot/apps/app@%s%d", dataset.ScheduledSnapPrefix, scheduledTimeForDay(catchUp, "02:00").Unix())
	s.runDailyRestorePointsPass(history.ByAutomatic, catchUp)
	if !slices.Equal(snapNames, []string{wantScheduled}) {
		t.Fatalf("first automatic pass created %v, want [%s]", snapNames, wantScheduled)
	}

	// Second automatic pass for the same day (e.g., after daemon restart without state)
	// sees the snapshot already exists and creates no second snapshot.
	s.rpSched.auto = rpRunRecord{}
	s.rpSched.last = rpRunRecord{}
	rec2 := s.runDailyRestorePointsPass(history.ByAutomatic, catchUp.Add(time.Hour))
	if rec2.Saved != 1 || len(snapNames) != 1 {
		t.Fatalf("second automatic pass: rec=%+v snapNames=%v", rec2, snapNames)
	}

	// Manual "Run now" the same day uses current time and creates its own snapshot.
	manualAt := catchUp.Add(2 * time.Hour)
	wantManual := fmt.Sprintf("zroot/apps/app@%s%d", dataset.ScheduledSnapPrefix, manualAt.UTC().Unix())
	recM := s.runDailyRestorePointsPass(history.ByYou, manualAt)
	if recM.Saved != 1 || len(snapNames) != 2 || snapNames[1] != wantManual {
		t.Fatalf("manual pass: rec=%+v snapNames=%v, want second %s", recM, snapNames, wantManual)
	}
}

func TestNotCoveredPaths(t *testing.T) {
	inDatasetOnly := &stack.Stack{
		Name: "app",
		Compose: strings.Join([]string{
			"services:",
			"  web:",
			"    image: x",
			"    volumes:",
			"      - /apps/app/config:/config",
			"      - /apps/app/data:/data",
		}, "\n"),
	}
	if got := notCoveredPaths(inDatasetOnly, "/apps/app"); len(got) != 0 {
		t.Fatalf("in-dataset only got %v, want empty", got)
	}

	withMedia := &stack.Stack{
		Name: "radarr",
		Compose: strings.Join([]string{
			"services:",
			"  radarr:",
			"    image: x",
			"    volumes:",
			"      - /apps/radarr/config:/config",
			"      - /mnt/movies:/movies",
			"      - /mnt/downloads:/downloads",
			"      - /etc/localtime:/etc/localtime:ro",
			"      - /var/run/podman.sock:/var/run/docker.sock",
			"      - radarr_cache:/cache",
			"volumes:",
			"  radarr_cache:",
		}, "\n"),
	}
	want := []string{"/mnt/downloads", "/mnt/movies", "radarr_cache"}
	if got := notCoveredPaths(withMedia, "/apps/radarr"); !slices.Equal(got, want) {
		t.Fatalf("withMedia got %v, want %v", got, want)
	}
}

func TestSnapshotBeforeUpdatePolicyAndRetention(t *testing.T) {
	root := t.TempDir()
	be := &stubUpdateBackend{Backend: engine.Unavailable("podman")}
	s := &server{
		manager:   stack.NewManager(root + "/stacks"),
		fjordRoot: root,
		auto:      newAutoState(root),
		rpSched:   newRPScheduleState(root),
		backends:  map[string]engine.Backend{"podman": be},
		defEngine: "podman",
	}
	days := 14
	if err := updateSettings(root, func(st *savedSettings) {
		st.RestorePointRetentionDays = &days
	}); err != nil {
		t.Fatal(err)
	}

	origCompose := "services:\n  web:\n    image: ghcr.io/example/app:2.0\n"
	if err := s.manager.Save(&stack.Stack{Name: "app", Compose: origCompose}); err != nil {
		t.Fatal(err)
	}
	if err := s.manager.UpdateState("app", func(*stack.State) *stack.State {
		return &stack.State{
			SchemaVersion:      1,
			DesiredState:       "running",
			Engine:             "podman",
			Dataset:            "zroot/apps/app",
			RestorePointPolicy: "off",
		}
	}); err != nil {
		t.Fatal(err)
	}

	var snapCalls int
	var gotKeep int
	var pruneMaxAge time.Duration
	snapErr := errors.New("out of space")
	oldSnap := snapshotBeforeUpdateFn
	oldPrune := pruneAutomaticSnapshotsFn
	snapshotBeforeUpdateFn = func(_ context.Context, ds string, _ time.Time, keep int, _ ...string) (string, error) {
		snapCalls++
		gotKeep = keep
		if snapErr != nil {
			return "", snapErr
		}
		return ds + "@fjord-update-1", nil
	}
	pruneAutomaticSnapshotsFn = func(_ context.Context, _ string, maxAge time.Duration, _ time.Time) (int, error) {
		pruneMaxAge = maxAge
		return 0, nil
	}
	t.Cleanup(func() {
		snapshotBeforeUpdateFn = oldSnap
		pruneAutomaticSnapshotsFn = oldPrune
	})

	// 1. Policy "off": skips snapshot completely and bypasses the automatic-update guard.
	act := autoAction{
		Stack:    "app",
		Services: []string{"web"},
		Retags:   []autoRetag{{service: "web", tag: "2.1"}},
		Keys:     map[string]string{"web": "2.1"},
	}
	res := s.autoUpdateStack(act)
	if res.Result != "updated" {
		t.Fatalf("policy=off autoUpdateStack result = %+v, want updated", res)
	}
	if snapCalls != 0 || be.updateCalls != 1 {
		t.Fatalf("policy=off snapCalls=%d updateCalls=%d, want 0 and 1", snapCalls, be.updateCalls)
	}

	// 2. Policy "daily" with failing snapshot: still aborts automatic update via guard.
	if err := s.manager.SetRestorePointPolicy("app", "daily"); err != nil {
		t.Fatal(err)
	}
	res = s.autoUpdateStack(act)
	if res.Result != "failed" || !strings.Contains(res.Reason, "out of space") {
		t.Fatalf("policy=daily snapshot fail result = %+v, want failed out of space", res)
	}
	if snapCalls != 1 || be.updateCalls != 1 {
		t.Fatalf("policy=daily fail snapCalls=%d updateCalls=%d, want 1 and 1", snapCalls, be.updateCalls)
	}

	// 3. Policy "updates" with working snapshot: passes keep=-1 (no 3-snap cap)
	// and prunes by configured retentionDays (14d).
	snapErr = nil
	if err := s.manager.SetRestorePointPolicy("app", "updates"); err != nil {
		t.Fatal(err)
	}
	st, err := s.manager.Get("app")
	if err != nil {
		t.Fatal(err)
	}
	note, err := s.snapshotBeforeUpdate(context.Background(), st)
	if err != nil || !strings.Contains(note, "@fjord-update-1") {
		t.Fatalf("snapshotBeforeUpdate = %q, %v", note, err)
	}
	if gotKeep != -1 {
		t.Fatalf("snapshotBeforeUpdate passed keep=%d, want -1 (age retention only)", gotKeep)
	}
	if pruneMaxAge != 14*24*time.Hour {
		t.Fatalf("pruneAutomaticSnapshotsFn maxAge=%v, want 14d", pruneMaxAge)
	}
}

func TestHandleRestorePointRunDetached(t *testing.T) {
	root := t.TempDir()
	s := &server{
		manager:   stack.NewManager(root + "/stacks"),
		fjordRoot: root,
		rpSched:   newRPScheduleState(root),
	}

	// Busy state -> 409 Conflict.
	s.rpSched.mu.Lock()
	s.rpSched.running = true
	s.rpSched.mu.Unlock()
	rec := httptest.NewRecorder()
	s.handleRestorePointSettings(rec, httptest.NewRequest(http.MethodPost, "/api/settings/restore-points/run", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("busy POST /run: got %d, want 409", rec.Code)
	}

	// Idle state -> 202 Accepted immediately.
	s.rpSched.mu.Lock()
	s.rpSched.running = false
	s.rpSched.mu.Unlock()
	rec = httptest.NewRecorder()
	s.handleRestorePointSettings(rec, httptest.NewRequest(http.MethodPost, "/api/settings/restore-points/run", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("idle POST /run: got %d, want 202", rec.Code)
	}
	// Wait briefly for the detached empty pass to finish so TempDir cleanup is clean.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.rpSched.mu.Lock()
		running := s.rpSched.running
		at := s.rpSched.last.At
		s.rpSched.mu.Unlock()
		if !running && !at.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStackSnapshotsKeepForeverHandler(t *testing.T) {
	root := t.TempDir()
	s := &server{
		manager:   stack.NewManager(root + "/stacks"),
		fjordRoot: root,
		rpSched:   newRPScheduleState(root),
	}
	if err := s.manager.Save(&stack.Stack{Name: "app", Compose: "services:\n  web:\n    image: x\n"}); err != nil {
		t.Fatal(err)
	}
	_ = s.manager.UpdateState("app", func(*stack.State) *stack.State {
		return &stack.State{SchemaVersion: 1, DesiredState: "running", Dataset: "zroot/apps/app"}
	})

	var calls []string
	oldKeep := setSnapshotKeepFn
	setSnapshotKeepFn = func(_ context.Context, ds, snap, stackName string, keep bool) error {
		calls = append(calls, fmt.Sprintf("%s|%s|%s|%v", ds, snap, stackName, keep))
		return nil
	}
	t.Cleanup(func() { setSnapshotKeepFn = oldKeep })

	// Missing snapshot -> 400.
	rec := httptest.NewRecorder()
	s.stackSnapshots(rec, httptest.NewRequest(http.MethodPatch, "/api/stacks/app/snapshots", strings.NewReader(`{"forever":true}`)), "app")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing snapshot: got %d, want 400", rec.Code)
	}

	// Non-fjord snapshot -> 400.
	rec = httptest.NewRecorder()
	s.stackSnapshots(rec, httptest.NewRequest(http.MethodPatch, "/api/stacks/app/snapshots",
		strings.NewReader(`{"snapshot":"zroot/apps/app@user-snap","forever":true}`)), "app")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-fjord snapshot: got %d, want 400", rec.Code)
	}

	// Keep forever -> 204.
	rec = httptest.NewRecorder()
	s.stackSnapshots(rec, httptest.NewRequest(http.MethodPatch, "/api/stacks/app/snapshots",
		strings.NewReader(`{"snapshot":"zroot/apps/app@fjord-update-1","forever":true}`)), "app")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("keep true: got %d %s", rec.Code, rec.Body)
	}

	// Stop keeping -> 204.
	rec = httptest.NewRecorder()
	s.stackSnapshots(rec, httptest.NewRequest(http.MethodPatch, "/api/stacks/app/snapshots",
		strings.NewReader(`{"snapshot":"zroot/apps/app@fjord-update-1","forever":false}`)), "app")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("keep false: got %d %s", rec.Code, rec.Body)
	}

	want := []string{
		"zroot/apps/app|zroot/apps/app@fjord-update-1|app|true",
		"zroot/apps/app|zroot/apps/app@fjord-update-1|app|false",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}
