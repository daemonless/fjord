package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/dataset"
	"github.com/daemonless/fjord/pkg/history"
	"github.com/daemonless/fjord/pkg/stack"
)

const (
	rpPolicyOff     = "off"
	rpPolicyUpdates = "updates"
	rpPolicyDaily   = "daily"

	defaultRPRetentionDays = 30
	defaultRPDailyTime     = "02:00"
	rpBusyRetryInterval    = 10 * time.Minute
)

// Dataset hooks used by runDailyRestorePointsPass; tests replace them.
var (
	snapshotScheduledFn = dataset.SnapshotScheduled
	writtenSinceFn      = dataset.WrittenSince
	listSnapshotsFn     = dataset.ListSnapshots
	snapshotExistsFn    = dataset.SnapshotExists
)

func validRPPolicy(p string) bool {
	return p == rpPolicyOff || p == rpPolicyUpdates || p == rpPolicyDaily
}

func validRPRetentionDays(d int) bool {
	return d == 7 || d == 14 || d == 30 || d == 90
}

func restorePointSettingsOf(st savedSettings) (defaultPolicy string, retentionDays int, dailyTime string) {
	defaultPolicy = rpPolicyUpdates
	if validRPPolicy(st.RestorePointDefault) {
		defaultPolicy = st.RestorePointDefault
	}
	retentionDays = defaultRPRetentionDays
	if st.RestorePointRetentionDays != nil && validRPRetentionDays(*st.RestorePointRetentionDays) {
		retentionDays = *st.RestorePointRetentionDays
	}
	dailyTime = defaultRPDailyTime
	if _, ok := parseHM(st.RestorePointDailyTime); ok {
		dailyTime = st.RestorePointDailyTime
	}
	return defaultPolicy, retentionDays, dailyTime
}

func effectiveRestorePointPolicy(st *stack.State) string {
	if st != nil && validRPPolicy(st.RestorePointPolicy) {
		return st.RestorePointPolicy
	}
	return rpPolicyUpdates
}

type rpRunRecord struct {
	At          time.Time `json:"at,omitempty"`
	Day         string    `json:"day,omitempty"` // YYYY-MM-DD local date when scheduled run fired
	Trigger     string    `json:"trigger,omitempty"`
	Saved       int       `json:"saved"`
	Skipped     int       `json:"skipped"`
	Failed      int       `json:"failed"`
	Busy        []string  `json:"busy,omitempty"`        // apps busy during today's scheduled pass; retried every 10m
	Done        []string  `json:"done,omitempty"`        // apps already saved/skipped/failed for Day
	NextRetryAt time.Time `json:"nextRetryAt,omitempty"` // earliest time to retry Busy apps
	Errors      []string  `json:"errors,omitempty"`
}

type rpStateFile struct {
	Last rpRunRecord `json:"last"`
	Auto rpRunRecord `json:"auto"`
}

type rpScheduleState struct {
	mu      sync.Mutex
	running bool
	last    rpRunRecord // latest run (automatic or manual), shown in UI
	auto    rpRunRecord // today's automatic run & busy-retry state, never clobbered by manual runs
	path    string
}

func newRPScheduleState(fjordRoot string) *rpScheduleState {
	st := &rpScheduleState{path: filepath.Join(fjordRoot, "restorepoints.json")}
	if b, err := os.ReadFile(st.path); err == nil {
		var f rpStateFile
		if err := json.Unmarshal(b, &f); err == nil && (!f.Last.At.IsZero() || f.Auto.Day != "" || f.Last.Day != "") {
			st.last = f.Last
			st.auto = f.Auto
		} else {
			// Legacy flat rpRunRecord format.
			_ = json.Unmarshal(b, &st.last)
			if st.last.Day != "" {
				st.auto = st.last
			}
		}
	}
	return st
}

func (r *rpScheduleState) save() {
	b, err := json.MarshalIndent(rpStateFile{Last: r.last, Auto: r.auto}, "", "  ")
	if err == nil {
		err = stack.WriteFileAtomic(r.path, append(b, '\n'), 0o600)
	}
	if err != nil {
		log.Printf("daily restore points: record last run: %v", err)
	}
}

func scheduledTimeForDay(now time.Time, hm string) time.Time {
	mins, ok := parseHM(hm)
	if !ok {
		mins = 2 * 60
	}
	y, m, d := now.Date()
	return time.Date(y, m, d, mins/60, mins%60, 0, 0, now.Location())
}

func nextDailyTime(now time.Time, hm string, ranToday bool) time.Time {
	cand := scheduledTimeForDay(now, hm)
	if ranToday || !cand.After(now) {
		cand = cand.AddDate(0, 0, 1)
	}
	return cand
}

func (s *server) restorePointCounts() (ready, daily, total int) {
	stacks, err := s.manager.List()
	if err != nil {
		return 0, 0, 0
	}
	for _, st := range stacks {
		state, _ := s.manager.LoadState(st.Name)
		if state != nil && state.DesiredState == "deleting" {
			continue
		}
		total++
		if state != nil && state.Dataset != "" {
			ready++
			if effectiveRestorePointPolicy(state) == rpPolicyDaily {
				daily++
			}
		}
	}
	return ready, daily, total
}

// handleRestorePointSettings serves GET/POST /api/settings/restore-points and
// POST /api/settings/restore-points/run.
func (s *server) handleRestorePointSettings(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/run") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.rpSched.mu.Lock()
		busy := s.rpSched.running
		s.rpSched.mu.Unlock()
		if busy {
			http.Error(w, "restore points are already running", http.StatusConflict)
			return
		}
		trigger := triggerOf(r)
		go s.runDailyRestorePointsPass(trigger, time.Now())
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch r.Method {
	case http.MethodGet:
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		defPolicy, retDays, dailyTime := restorePointSettingsOf(loadSettings(s.fjordRoot))
		ready, daily, total := s.restorePointCounts()
		s.rpSched.mu.Lock()
		last, autoDay, running := s.rpSched.last, s.rpSched.auto.Day, s.rpSched.running
		s.rpSched.mu.Unlock()
		now := time.Now()
		ranToday := autoDay == now.Format("2006-01-02")
		next := ""
		if daily > 0 {
			next = nextDailyTime(now, dailyTime, ranToday).Format(time.RFC3339)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"hasZfs":        dataset.HasZFS(ctx),
			"defaultPolicy": defPolicy,
			"retentionDays": retDays,
			"dailyTime":     dailyTime,
			"readyCount":    ready,
			"dailyCount":    daily,
			"totalCount":    total,
			"running":       running,
			"next":          next,
			"last":          last,
		})

	case http.MethodPost:
		var req struct {
			DefaultPolicy *string `json:"defaultPolicy"`
			RetentionDays *int    `json:"retentionDays"`
			DailyTime     *string `json:"dailyTime"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", http.StatusBadRequest)
			return
		}
		if req.DefaultPolicy == nil && req.RetentionDays == nil && req.DailyTime == nil {
			http.Error(w, "defaultPolicy, retentionDays, or dailyTime required", http.StatusBadRequest)
			return
		}
		if req.DefaultPolicy != nil && !validRPPolicy(*req.DefaultPolicy) {
			http.Error(w, "defaultPolicy must be off, updates, or daily", http.StatusBadRequest)
			return
		}
		if req.RetentionDays != nil && !validRPRetentionDays(*req.RetentionDays) {
			http.Error(w, "retentionDays must be 7, 14, 30, or 90", http.StatusBadRequest)
			return
		}
		if req.DailyTime != nil {
			if _, ok := parseHM(*req.DailyTime); !ok {
				http.Error(w, "dailyTime must be HH:MM like 02:00", http.StatusBadRequest)
				return
			}
		}
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) {
			if req.DefaultPolicy != nil {
				st.RestorePointDefault = *req.DefaultPolicy
			}
			if req.RetentionDays != nil {
				v := *req.RetentionDays
				st.RestorePointRetentionDays = &v
			}
			if req.DailyTime != nil {
				st.RestorePointDailyTime = *req.DailyTime
			}
		}); err != nil {
			http.Error(w, "persist: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// stackRestorePointPolicy handles POST /api/stacks/<name>/restore-points/policy.
func (s *server) stackRestorePointPolicy(w http.ResponseWriter, r *http.Request, name string) {
	if _, err := s.manager.Get(name); err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}
	var req struct {
		Policy string `json:"policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !validRPPolicy(req.Policy) {
		http.Error(w, `send {"policy": "off" | "updates" | "daily"}`, http.StatusBadRequest)
		return
	}
	if err := s.manager.SetRestorePointPolicy(name, req.Policy); err != nil {
		http.Error(w, "persist: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if req.Policy != rpPolicyDaily {
		s.clearFailureNotice(name, stack.NoticeDailyRestorePoint)
	}
	w.WriteHeader(http.StatusNoContent)
}

// finalizeExpiredBusy closes out any apps that remained busy at the end of an
// earlier local day as a single failure ("busy all day") per app.
func (s *server) finalizeExpiredBusy(now time.Time) {
	today := now.Format("2006-01-02")
	s.rpSched.mu.Lock()
	if s.rpSched.running || s.rpSched.auto.Day == "" || s.rpSched.auto.Day >= today || len(s.rpSched.auto.Busy) == 0 {
		s.rpSched.mu.Unlock()
		return
	}
	expired := append([]string(nil), s.rpSched.auto.Busy...)
	s.rpSched.auto.Busy = nil
	s.rpSched.auto.NextRetryAt = time.Time{}
	for _, name := range expired {
		s.rpSched.auto.Failed++
		s.rpSched.auto.Errors = append(s.rpSched.auto.Errors, name+": busy all day")
	}
	s.rpSched.last = s.rpSched.auto
	s.rpSched.save()
	s.rpSched.mu.Unlock()

	for _, name := range expired {
		log.Printf("daily restore point %s: busy all day", name)
		if s.recordFailureNotice(name, stack.NoticeDailyRestorePoint, "busy all day", now) {
			n := newNote(history.Snapshot, history.ByAutomatic)
			n.detail = "Daily restore point failed: busy all day"
			s.writeHistory(name, n, true, n.detail)
		}
	}
}

// runDailyRestorePointsPass snapshots every ZFS-dataset stack whose restore
// point policy is "daily", taking each stack's operation lock so it never races
// a restore or dataset migration.
//   - Skips taking a snapshot when ZFS reports written@<newest fjord snap> == 0
//     (still prunes), unaffected by snapshots created by external tools.
//   - Records busy apps for same-day retries (every 10 minutes); apps already
//     saved or skipped today are not touched again. Manual runs report busy
//     apps without mixing counts into the automatic day's retry record.
//   - Automatic passes name snapshots by today's scheduled time (idempotent if
//     already present); manual "Run now" uses the current time.
func (s *server) runDailyRestorePointsPass(trigger string, now time.Time) rpRunRecord {
	if trigger == history.ByAutomatic {
		s.finalizeExpiredBusy(now)
	}

	today := now.Format("2006-01-02")
	s.rpSched.mu.Lock()
	if s.rpSched.running {
		last := s.rpSched.last
		s.rpSched.mu.Unlock()
		return last
	}
	s.rpSched.running = true
	prevAuto := s.rpSched.auto
	s.rpSched.mu.Unlock()
	defer func() {
		s.rpSched.mu.Lock()
		s.rpSched.running = false
		s.rpSched.mu.Unlock()
	}()

	_, retDays, dailyTime := restorePointSettingsOf(loadSettings(s.fjordRoot))
	maxAge := time.Duration(retDays) * 24 * time.Hour

	isRetry := trigger == history.ByAutomatic && prevAuto.Day == today && len(prevAuto.Busy) > 0
	rec := rpRunRecord{At: now.UTC(), Trigger: trigger}
	doneSet := map[string]bool{}
	if isRetry {
		rec.Day = prevAuto.Day
		rec.Saved = prevAuto.Saved
		rec.Skipped = prevAuto.Skipped
		rec.Failed = prevAuto.Failed
		rec.Errors = append([]string(nil), prevAuto.Errors...)
		rec.Done = append([]string(nil), prevAuto.Done...)
		for _, name := range prevAuto.Done {
			doneSet[name] = true
		}
	} else if trigger == history.ByAutomatic {
		rec.Day = today
	}

	snapAt := now.UTC()
	if trigger == history.ByAutomatic {
		snapAt = scheduledTimeForDay(now, dailyTime)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	stacks, err := s.manager.List()
	if err != nil {
		log.Printf("daily restore points: list stacks: %v", err)
		return rec
	}
	type rpTarget struct {
		st    *stack.Stack
		state *stack.State
	}
	var targets []rpTarget
	for _, st := range stacks {
		if isRetry && doneSet[st.Name] {
			continue
		}
		state, _ := s.manager.LoadState(st.Name)
		if state == nil || state.Dataset == "" || state.DesiredState == "deleting" {
			continue
		}
		if effectiveRestorePointPolicy(state) != rpPolicyDaily {
			continue
		}
		targets = append(targets, rpTarget{st: st, state: state})
	}

	var jobID string
	if len(targets) > 0 || trigger != history.ByAutomatic {
		items := make([]bgJobItem, len(targets))
		for i, t := range targets {
			items[i] = bgJobItem{Name: t.st.Name, Status: "queued"}
		}
		jobID = s.jobs.start("restore-points", "Saving daily restore points", false, "Checking apps", items)
	}

	passFailed := 0
	for _, tgt := range targets {
		st, state := tgt.st, tgt.state
		s.jobs.setPhase(jobID, st.Name, "Checking changes", 0, 0)
		unlock, ok := lockStackAs(st.Name, "snapshot")
		if !ok {
			log.Printf("daily restore point %s: skipped (busy)", st.Name)
			rec.Busy = append(rec.Busy, st.Name)
			s.jobs.recordItem(jobID, st.Name, "skipped", "Busy")
			continue
		}

		var snapErr error
		skipped := false
		alreadyExists := false
		if trigger == history.ByAutomatic {
			expectedSnap := fmt.Sprintf("%s@%s%d", state.Dataset, dataset.ScheduledSnapPrefix, snapAt.Unix())
			if snapshotExistsFn(ctx, expectedSnap) {
				alreadyExists = true
			}
		}

		if !alreadyExists {
			if snaps, lerr := listSnapshotsFn(ctx, state.Dataset, false); lerr == nil && len(snaps) > 0 {
				newest := snaps[len(snaps)-1].Name
				if written, werr := writtenSinceFn(ctx, state.Dataset, newest); werr == nil && written == 0 {
					skipped = true
				}
			}
		}

		if !alreadyExists && !skipped {
			s.jobs.setPhase(jobID, st.Name, "Saving restore point", 0, 0)
			_, imagesJSON := currentStackVersionImage(ctx, s, st)
			_, snapErr = snapshotScheduledFn(ctx, state.Dataset, snapAt, imagesJSON)
		}
		if snapErr == nil {
			_, _ = pruneAutomaticSnapshotsFn(ctx, state.Dataset, maxAge, now.UTC())
		}
		unlock()

		if trigger == history.ByAutomatic {
			rec.Done = append(rec.Done, st.Name)
		}
		if snapErr != nil {
			rec.Failed++
			passFailed++
			rec.Errors = append(rec.Errors, st.Name+": "+snapErr.Error())
			log.Printf("daily restore point %s: %v", st.Name, snapErr)
			s.jobs.recordItem(jobID, st.Name, "failed", history.FirstLine(snapErr.Error()))
			writeHist := true
			if trigger == history.ByAutomatic {
				writeHist = s.recordFailureNotice(st.Name, stack.NoticeDailyRestorePoint, snapErr.Error(), now)
			}
			if writeHist {
				n := newNote(history.Snapshot, trigger)
				n.detail = fmt.Sprintf("Daily restore point failed: %v", snapErr)
				s.writeHistory(st.Name, n, true, n.detail)
			}
			continue
		}
		if cleared := s.clearFailureNotice(st.Name, stack.NoticeDailyRestorePoint); cleared != nil {
			n := newNote(history.Snapshot, trigger)
			n.detail = "Daily restore point succeeded again"
			s.writeHistory(st.Name, n, false, "")
		}
		if skipped {
			rec.Skipped++
			s.jobs.recordItem(jobID, st.Name, "skipped", "No changes")
		} else {
			rec.Saved++
			s.jobs.recordItem(jobID, st.Name, "done", "Saved")
		}
	}

	if len(rec.Busy) > 0 {
		slices.Sort(rec.Busy)
		if trigger == history.ByAutomatic {
			rec.NextRetryAt = now.Add(rpBusyRetryInterval)
		}
	}

	s.rpSched.mu.Lock()
	if trigger == history.ByAutomatic {
		s.rpSched.auto = rec
	} else {
		rec.Day = s.rpSched.auto.Day
	}
	s.rpSched.last = rec
	s.rpSched.save()
	s.rpSched.mu.Unlock()
	if rec.Saved > 0 || rec.Skipped > 0 || rec.Failed > 0 || len(rec.Busy) > 0 {
		log.Printf("daily restore points (%s): %d saved, %d skipped, %d failed, %d busy",
			trigger, rec.Saved, rec.Skipped, rec.Failed, len(rec.Busy))
	}
	if jobID != "" {
		if trigger == history.ByAutomatic && passFailed == 0 {
			s.jobs.remove(jobID)
		} else {
			var parts []string
			if rec.Saved > 0 {
				parts = append(parts, fmt.Sprintf("%d saved", rec.Saved))
			}
			if rec.Skipped > 0 {
				parts = append(parts, fmt.Sprintf("%d unchanged", rec.Skipped))
			}
			if len(rec.Busy) > 0 {
				parts = append(parts, fmt.Sprintf("%d busy", len(rec.Busy)))
			}
			if rec.Failed > 0 {
				parts = append(parts, fmt.Sprintf("%d failed", rec.Failed))
			}
			summary := "No apps set to Daily"
			if len(parts) > 0 {
				summary = "Restore points: " + strings.Join(parts, ", ")
			}
			if rec.Failed > 0 {
				s.jobs.finish(jobID, "failed", summary)
			} else {
				s.jobs.finish(jobID, "done", summary)
			}
		}
	}
	return rec
}

// shouldRunDailyRestorePoints reports whether the daily schedule is due at now:
// once per local calendar day at or after dailyTime (same-day catch-up), plus
// same-day retries every 10 minutes while any app was busy.
func (s *server) shouldRunDailyRestorePoints(now time.Time) bool {
	s.finalizeExpiredBusy(now)
	_, _, dailyTime := restorePointSettingsOf(loadSettings(s.fjordRoot))
	mins, ok := parseHM(dailyTime)
	if !ok {
		return false
	}
	today := now.Format("2006-01-02")
	s.rpSched.mu.Lock()
	defer s.rpSched.mu.Unlock()
	if s.rpSched.running {
		return false
	}
	if s.rpSched.auto.Day == today {
		return len(s.rpSched.auto.Busy) > 0 && !now.Before(s.rpSched.auto.NextRetryAt)
	}
	return now.Hour()*60+now.Minute() >= mins
}

// runDailyRestorePoints checks every minute whether the daily schedule time
// has arrived (or a busy app is due for retry) and runs the pass.
func runDailyRestorePoints(s *server) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-daemonCtx.Done():
			return
		case now := <-t.C:
			if s.shouldRunDailyRestorePoints(now) {
				s.runDailyRestorePointsPass(history.ByAutomatic, now)
			}
		}
	}
}
