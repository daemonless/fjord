package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/history"
	"github.com/daemonless/fjord/pkg/registry"
	"github.com/daemonless/fjord/pkg/stack"
	"github.com/daemonless/fjord/pkg/updates"
)

// Automatic updates: once a night, inside the update window, every stack
// whose policy lets them gets the updates its policy takes, one stack at a
// time, through the same update the button runs (rollback record, snapshot,
// health watch, History). The fleet check that finds them already runs
// every half hour (runFleetScheduler), so what an update installs has been
// seen, and soaked, long before the window opens.

const (
	autoWindowStart = "03:00"
	autoWindowEnd   = "05:00"
)

// autoWindowOf is the update window as saved, or the default.
func autoWindowOf(st savedSettings) (start, end string) {
	start, end = autoWindowStart, autoWindowEnd
	if _, ok := parseHM(st.AutoUpdateStart); ok {
		start = st.AutoUpdateStart
	}
	if _, ok := parseHM(st.AutoUpdateEnd); ok {
		end = st.AutoUpdateEnd
	}
	return start, end
}

// parseHM reads "03:00" as minutes after midnight.
func parseHM(s string) (int, bool) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) != 2 || len(m) != 2 {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

// windowAt says whether now is inside the window, and when the window now
// is (or the next one) opened. A window may cross midnight (23:00-01:00).
func windowAt(now time.Time, start, end string) (inside bool, opened time.Time) {
	s, _ := parseHM(start)
	e, _ := parseHM(end)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	at := func(d time.Time, min int) time.Time { return d.Add(time.Duration(min) * time.Minute) }
	cur := now.Hour()*60 + now.Minute()
	switch {
	case s == e:
		return false, at(day, s)
	case s < e:
		if cur >= s && cur < e {
			return true, at(day, s)
		}
		if cur < s {
			return false, at(day, s)
		}
		return false, at(day.AddDate(0, 0, 1), s)
	default: // crosses midnight
		if cur >= s {
			return true, at(day, s)
		}
		if cur < e {
			return true, at(day.AddDate(0, 0, -1), s)
		}
		return false, at(day, s)
	}
}

// autoResult is what happened to one stack in a run.
type autoResult struct {
	Stack    string   `json:"stack"`
	Services []string `json:"services,omitempty"`
	Result   string   `json:"result"` // updated | rolled back | failed | waiting | skipped
	Reason   string   `json:"reason,omitempty"`
}

// autoRun is the last run, kept in <fjordRoot>/autoupdate.json.
type autoRun struct {
	At      time.Time    `json:"at,omitempty"`
	Trigger string       `json:"trigger,omitempty"`
	Window  time.Time    `json:"window,omitempty"` // when the window it ran in opened
	Results []autoResult `json:"results,omitempty"`
	// Paused: automatic updates were paused, before or during this run.
	Paused bool `json:"paused,omitempty"`
}

// autoFile is what <fjordRoot>/autoupdate.json keeps.
type autoFile struct {
	Last autoRun `json:"last"`
	// Failures are when automatic updates failed lately; two within a day
	// pause them all (PausedAt) until someone resumes: a broken base image
	// should cost one app, not twelve.
	Failures []time.Time `json:"failures,omitempty"`
	PausedAt time.Time   `json:"pausedAt,omitempty"`
}

// autoBreaker is how many automatic failures within autoBreakerSpan pause
// automatic updates for the whole host.
const (
	autoBreaker     = 2
	autoBreakerSpan = 24 * time.Hour
)

type autoState struct {
	mu      sync.Mutex
	running bool
	f       autoFile
	path    string
}

func newAutoState(fjordRoot string) *autoState {
	a := &autoState{path: filepath.Join(fjordRoot, "autoupdate.json")}
	if b, err := os.ReadFile(a.path); err == nil {
		_ = json.Unmarshal(b, &a.f)
	}
	return a
}

// failed counts an automatic failure; true when it pauses them all. Caller
// holds mu.
func (a *autoState) failed(now time.Time) bool {
	var recent []time.Time
	for _, t := range a.f.Failures {
		if now.Sub(t) < autoBreakerSpan {
			recent = append(recent, t)
		}
	}
	a.f.Failures = append(recent, now)
	if len(a.f.Failures) >= autoBreaker && a.f.PausedAt.IsZero() {
		a.f.PausedAt = now
		return true
	}
	return false
}

func (a *autoState) save() {
	b, err := json.MarshalIndent(a.f, "", "  ")
	if err == nil {
		err = stack.WriteFileAtomic(a.path, b, 0o600)
	}
	if err != nil {
		log.Printf("auto-update: record last run: %v", err)
	}
}

// autoService is one service with an update, as the planner needs it.
type autoService struct {
	Name   string
	Policy updates.Policy
	Cand   updates.Candidate
	NewTag string // a newer version's tag, when the update is a version bump
	Key    string // what the update installs (first-seen and hold key)
	// Held: this very update failed here before and was rolled back.
	Held string // when, for the reason line; "" when not held
	// Unhold is the image to give the service back before updating it: a
	// service held on a digest after a failed update, now offered a newer one.
	Unhold string
	From   string // what it runs, and what the update installs: versions,
	To     string // or tags, for the History line and the banner
}

// autoStack is one stack as the planner sees it.
type autoStack struct {
	Name       string
	Stopped    bool // stopped on purpose
	Pending    bool // saved changes not applied yet: an update would apply them
	PerService bool // the engine updates one service at a time, and watches it
	Services   []autoService
}

type autoRetag struct{ service, tag string }

// autoAction is what the planner decided for one stack: the services to
// update (and retag first), or why nothing.
type autoAction struct {
	Stack    string
	Services []string
	Retags   []autoRetag
	Unholds  map[string]string // service -> image to give back before updating
	Keys     map[string]string // service -> what its update installs
	From, To string            // for the banner, when it fails
	Result   string            // "" (act), waiting, skipped
	Reason   string
}

// planAuto decides one stack. A stack none of whose services take
// automatic updates gets Result "" and no services: not listed.
func planAuto(st autoStack, now time.Time) autoAction {
	act := autoAction{Stack: st.Name}
	takes := false
	for _, sv := range st.Services {
		if sv.Policy != updates.Off && sv.Policy != updates.Notify && sv.Policy != "" {
			takes = true
		}
	}
	if !takes || len(st.Services) == 0 {
		return act
	}
	switch {
	case st.Stopped:
		act.Result, act.Reason = "skipped", "stopped"
		return act
	case st.Pending:
		act.Result, act.Reason = "skipped", "it has saved changes not applied yet"
		return act
	case !st.PerService:
		act.Result, act.Reason = "skipped", "its engine can't update one service at a time and watch it"
		return act
	}
	var waiting, from, to []string
	for _, sv := range st.Services {
		if sv.Held != "" {
			waiting = append(waiting, sv.Name+": its update failed on "+sv.Held+" and was rolled back; waiting for a newer one")
			continue
		}
		v := updates.Decide(sv.Policy, sv.Cand, now)
		if !v.Act {
			waiting = append(waiting, sv.Name+": "+v.Reason)
			continue
		}
		act.Services = append(act.Services, sv.Name)
		if sv.NewTag != "" {
			act.Retags = append(act.Retags, autoRetag{sv.Name, sv.NewTag})
		}
		if sv.Unhold != "" {
			if act.Unholds == nil {
				act.Unholds = map[string]string{}
			}
			act.Unholds[sv.Name] = sv.Unhold
		}
		if act.Keys == nil {
			act.Keys = map[string]string{}
		}
		act.Keys[sv.Name] = sv.Key
		if sv.From != "" {
			from = append(from, sv.From)
		}
		if sv.To != "" {
			to = append(to, sv.To)
		}
	}
	act.From, act.To = strings.Join(from, ", "), strings.Join(to, ", ")
	if len(act.Services) == 0 {
		act.Result, act.Reason = "waiting", strings.Join(waiting, "; ")
	}
	return act
}

// runAutoUpdateScheduler runs the night's updates once per window.
func runAutoUpdateScheduler(s *server) {
	for {
		start, end := autoWindowOf(loadSettings(s.fjordRoot))
		inside, opened := windowAt(time.Now(), start, end)
		s.auto.mu.Lock()
		due := inside && !s.auto.running && s.auto.f.Last.Window.Before(opened)
		s.auto.mu.Unlock()
		if due {
			s.autoUpdate(history.ByAutomatic, opened, end)
		}
		select {
		case <-daemonCtx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// autoUpdate runs one pass. Started by the window, it stops starting new
// stacks once the window closes (the one running finishes); Run now runs
// through. Returns false when a pass is already running.
func (s *server) autoUpdate(trigger string, window time.Time, end string) bool {
	s.auto.mu.Lock()
	if s.auto.running {
		s.auto.mu.Unlock()
		return false
	}
	s.auto.running = true
	s.auto.mu.Unlock()
	defer func() {
		s.auto.mu.Lock()
		s.auto.running = false
		s.auto.mu.Unlock()
	}()

	run := autoRun{At: time.Now(), Trigger: trigger, Window: window}
	// Soak is judged as of when the window closes: a version first seen at
	// 03:10 is due the next night, not the one after (23h50m at 03:00).
	decideAt := time.Now()
	if e, ok := parseHM(end); ok {
		closes := time.Date(window.Year(), window.Month(), window.Day(), 0, 0, 0, 0, window.Location()).Add(time.Duration(e) * time.Minute)
		if !closes.After(window) {
			closes = closes.AddDate(0, 0, 1)
		}
		decideAt = closes
	}
	s.auto.mu.Lock()
	paused := !s.auto.f.PausedAt.IsZero()
	s.auto.mu.Unlock()
	if paused && trigger == history.ByAutomatic {
		run.Paused = true
		s.auto.mu.Lock()
		s.auto.f.Last = run
		s.auto.save()
		s.auto.mu.Unlock()
		log.Printf("auto-update: paused, nothing done")
		return true
	}
	s.freshFleet()
	s.fleet.mu.Lock()
	cached := s.fleet.current(s.manager)
	s.fleet.mu.Unlock()
	names := make([]string, 0, len(cached))
	for n := range cached {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if trigger == history.ByAutomatic && end != "" {
			if inside, _ := windowAt(time.Now(), window.Format("15:04"), end); !inside {
				run.Results = append(run.Results, autoResult{Stack: name, Result: "waiting", Reason: "the window closed"})
				continue
			}
		}
		act, ok := s.planStack(name, cached[name], decideAt)
		if !ok || (act.Result == "" && len(act.Services) == 0) {
			continue
		}
		if act.Result != "" {
			run.Results = append(run.Results, autoResult{Stack: name, Result: act.Result, Reason: act.Reason})
			continue
		}
		if run.Paused {
			run.Results = append(run.Results, autoResult{Stack: name, Result: "waiting", Reason: "automatic updates are paused"})
			continue
		}
		r := s.autoUpdateStack(act)
		run.Results = append(run.Results, r)
		if r.Result == "failed" || r.Result == "rolled back" {
			s.auto.mu.Lock()
			if s.auto.failed(time.Now()) {
				run.Paused = true
				log.Printf("auto-update: %d failed within a day, paused", autoBreaker)
			}
			s.auto.mu.Unlock()
		}
	}
	s.auto.mu.Lock()
	s.auto.f.Last = run
	s.auto.save()
	s.auto.mu.Unlock()
	log.Printf("auto-update: %s", autoSummary(run.Results))
	return true
}

// freshFleet re-checks every stack now, waiting for a check already
// running instead of starting a second.
func (s *server) freshFleet() {
	for {
		s.fleet.mu.Lock()
		if !s.fleet.refreshing {
			s.fleet.refreshing = true
			s.fleet.mu.Unlock()
			s.refreshFleet()
			return
		}
		s.fleet.mu.Unlock()
		select {
		case <-daemonCtx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// planStack gathers one stack's services with updates and plans them.
func (s *server) planStack(name string, status updates.Status, at time.Time) (autoAction, bool) {
	st, err := s.manager.Get(name)
	if err != nil {
		return autoAction{}, false
	}
	state, _ := s.manager.LoadState(name)
	ctx, cancel := context.WithTimeout(daemonCtx, 2*time.Minute)
	defer cancel()
	in := autoStack{
		Name:       name,
		Stopped:    st.LeftStopped(),
		Pending:    len(s.pendingFor(st)) > 0,
		PerService: s.backendFor(st).Capabilities().UpdateServices,
	}
	running := map[string]updates.Service{}
	for _, sv := range s.updateServices(ctx, st) {
		running[sv.Name] = sv
	}
	var held map[string]string
	var since string
	if state != nil {
		held = state.AutoHold
		if state.AutoRollback != nil {
			since = state.AutoRollback.At.Local().Format("2006-01-02")
		}
	}
	for _, sv := range status.Services {
		p := policyFor(state, sv.Service)
		takes := p != updates.Off && p != updates.Notify && p != ""
		svc, isRunning := running[sv.Service]
		as := autoService{Name: sv.Service, Policy: p}
		switch {
		case sv.State == "available" || (sv.State == "upgrade" && sv.NewTag != ""):
			if sv.State == "upgrade" {
				as.NewTag = sv.NewTag
			}
		case sv.State == "pinned" && held[sv.Service] != "" && takes && isRunning:
			// Held on the digest it ran before a failed update: what the tag
			// names now decides. The same build waits; a newer one is a
			// fresh update, taken from the tag the compose had.
			svc.Image = withoutDigest(sv.Image)
			as.Unhold = withoutDigest(sv.Image)
			if state != nil {
				if rb, ok := state.Rollback[sv.Service]; ok && rb.Compose != "" {
					as.Unhold = rb.Compose
				}
			}
		default:
			continue
		}
		as.Cand = updates.Candidate{
			Class:      updates.Unknown,
			Dependency: len(composepkg.WithDependents(st.Compose, []string{sv.Service})) > 1,
			DataMajor:  updates.DataTiedToMajor(registry.Repo(sv.Image)),
		}
		if takes && isRunning {
			if c, err := s.classifyService(ctx, st, svc); err == nil {
				as.Cand.Class = c.Class
				as.Cand.FirstSeen = s.seen.Mark(c.Key)
				as.Key = c.Key
				as.From, as.To = c.Diff.VersionFrom, c.Diff.VersionTo
			} else if as.Unhold != "" {
				continue // can't tell what the tag names now: stays held
			}
		}
		if as.From == "" {
			as.From = sv.Tag
		}
		if as.To == "" {
			as.To = sv.NewTag
		}
		if as.Key != "" && held[sv.Service] == as.Key {
			as.Held, as.Unhold = cmp.Or(since, "an earlier night"), ""
		} else if as.Unhold != "" && as.Key == "" {
			continue
		}
		in.Services = append(in.Services, as)
	}
	return planAuto(in, at), true
}

// autoUpdateStack updates one stack as planned, under its lock, and rolls
// it back when the update fails.
func (s *server) autoUpdateStack(act autoAction) autoResult {
	res := autoResult{Stack: act.Stack, Services: act.Services}
	unlock, ok := lockStackAs(act.Stack, "update")
	if !ok {
		res.Result, res.Reason = "waiting", "busy with something else"
		return res
	}
	defer unlock()
	st, err := s.manager.Get(act.Stack)
	if err != nil {
		res.Result, res.Reason = "failed", err.Error()
		return res
	}
	// What a rollback puts back: a retag rewrites the compose and the .env.
	prevCompose, prevEnv := st.Compose, st.Env
	started := time.Now()
	if len(act.Unholds) > 0 {
		if _, err := s.manager.Update(act.Stack, func(fresh *stack.Stack) error {
			for svc, ref := range act.Unholds {
				if err := setServiceImage(fresh, svc, ref); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			res.Result, res.Reason = "failed", "give back its tag: "+err.Error()
			return res
		}
	}
	for _, rt := range act.Retags {
		if err := s.retagForUpdate(act.Stack, rt.service, rt.tag); err != nil {
			res.Result, res.Reason = "failed", "retag "+rt.service+": "+err.Error()
			s.restoreCompose(act.Stack, prevCompose, prevEnv)
			return res
		}
	}
	startErr := s.runDetachedUpdate(act.Stack, act.Services, history.ByAutomatic)
	state, _ := s.manager.LoadState(act.Stack)
	if state == nil || state.LastFailure == nil || state.LastFailure.At.Before(started) {
		res.Result = "updated"
		s.clearAutoHold(act.Stack, act.Services)
		return res
	}
	why := history.FirstLine(state.LastFailure.Message)
	// Aborted before pulling or stopping containers because taking the
	// pre-update restore point failed: put compose/.env back if retagged,
	// leave the rollback record untouched, and report the failure without
	// rolling back running containers.
	if errors.Is(startErr, errSnapshotAborted) {
		if len(act.Unholds) > 0 || len(act.Retags) > 0 {
			s.restoreCompose(act.Stack, prevCompose, prevEnv)
		}
		res.Result, res.Reason = "failed", why
		return res
	}
	back, backWhy := s.autoRollback(act, prevCompose, prevEnv, state)
	tags := map[string]string{}
	for _, rt := range act.Retags {
		tags[rt.service] = rt.tag
	}
	if err := s.manager.UpdateState(act.Stack, func(fresh *stack.State) *stack.State {
		if fresh == nil {
			return nil
		}
		if fresh.AutoHold == nil {
			fresh.AutoHold = map[string]string{}
		}
		for svc, key := range act.Keys {
			if key != "" {
				fresh.AutoHold[svc] = key
			}
		}
		fresh.AutoRollback = &stack.AutoRollback{At: time.Now(), Services: act.Services, From: act.From, To: act.To,
			Why: why, Back: back, BackWhy: backWhy, Tags: tags}
		return fresh
	}); err != nil {
		log.Printf("auto-update %s: record the rollback: %v", act.Stack, err)
	}
	if back {
		res.Result, res.Reason = "rolled back", why
	} else {
		res.Result, res.Reason = "failed", why+"; going back failed too: "+backWhy
	}
	return res
}

// autoRollback returns the services to what they ran before a failed
// automatic update: the compose and .env as they were (which undoes a
// version's retag), and a service that kept its tag held on the digest it
// ran, so the build that failed is not pulled back in. Never retried: a
// rollback that fails is said once.
func (s *server) autoRollback(act autoAction, prevCompose, prevEnv string, state *stack.State) (bool, string) {
	retagged := map[string]bool{}
	for _, rt := range act.Retags {
		retagged[rt.service] = true
	}
	if _, err := s.manager.Update(act.Stack, func(fresh *stack.Stack) error {
		fresh.Compose, fresh.Env = prevCompose, prevEnv
		for _, svc := range act.Services {
			if retagged[svc] {
				continue
			}
			rb, ok := state.Rollback[svc]
			if !ok || rb.Digest == "" {
				return fmt.Errorf("no earlier image of %s recorded", svc)
			}
			if err := setServiceImage(fresh, svc, withoutDigest(rb.Ref)+"@"+rb.Digest); err != nil {
				return err
			}
		}
		return directorFollowsCompose(fresh, act.Services)
	}); err != nil {
		return false, err.Error()
	}
	st, err := s.manager.Get(act.Stack)
	if err != nil {
		return false, err.Error()
	}
	rec, ok := s.backendFor(st).(engine.Recreator)
	if !ok {
		return false, "its engine can't recreate one service"
	}
	ctx, cancel := context.WithTimeout(daemonCtx, 20*time.Minute)
	defer cancel()
	started := time.Now()
	note := newNote(history.Rollback, history.ByAutomatic, act.Services...)
	note.detail = "after the automatic update failed"
	stream, err := rec.Recreate(watchable(ctx, act.Stack), st, act.Services)
	if err != nil {
		stream = errorStream(err.Error())
	}
	io.Copy(io.Discard, s.recordOutcome(act.Stack, "rollback", note, stream))
	stream.Close()
	s.fleet.forget(act.Stack)
	if after, _ := s.manager.LoadState(act.Stack); after != nil && after.LastFailure != nil && !after.LastFailure.At.Before(started) {
		return false, history.FirstLine(after.LastFailure.Message)
	}
	return true, ""
}

// restoreCompose puts a stack's compose and .env back, after a step that
// changed them failed before anything was recreated.
func (s *server) restoreCompose(name, compose, env string) {
	if _, err := s.manager.Update(name, func(fresh *stack.Stack) error {
		fresh.Compose, fresh.Env = compose, env
		return nil
	}); err != nil {
		log.Printf("auto-update %s: put its compose back: %v", name, err)
	}
}

// clearAutoHold forgets the holds (and the banner) of services that took
// an update: whatever failed before is behind them.
func (s *server) clearAutoHold(name string, services []string) {
	if err := s.manager.UpdateState(name, func(st *stack.State) *stack.State {
		if st == nil || (len(st.AutoHold) == 0 && st.AutoRollback == nil) {
			return nil
		}
		for _, svc := range services {
			delete(st.AutoHold, svc)
		}
		if st.AutoRollback != nil && len(st.AutoHold) == 0 {
			st.AutoRollback = nil
		}
		return st
	}); err != nil {
		log.Printf("auto-update %s: clear its hold: %v", name, err)
	}
}

// autoSummary is a run in one line, for the log.
func autoSummary(rs []autoResult) string {
	count := map[string]int{}
	for _, r := range rs {
		count[r.Result]++
	}
	if len(rs) == 0 {
		return "nothing to do"
	}
	var parts []string
	for _, k := range []string{"updated", "failed", "waiting", "skipped"} {
		if count[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count[k], k))
		}
	}
	return strings.Join(parts, ", ")
}

// handleAutoUpdate: GET the window, the last run and the next; POST
// {"start": "03:00", "end": "05:00"} sets the window; POST .../run (Run now)
// runs a pass at once, in the background.
func (s *server) handleAutoUpdate(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/offer") {
		s.autoUpdateOffer(w, r)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/all") {
		s.autoUpdateAll(w, r)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/resume") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.auto.mu.Lock()
		s.auto.f.PausedAt, s.auto.f.Failures = time.Time{}, nil
		s.auto.save()
		s.auto.mu.Unlock()
		log.Printf("auto-update: resumed")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/run") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.auto.mu.Lock()
		busy := s.auto.running
		s.auto.mu.Unlock()
		if busy {
			http.Error(w, "automatic updates are already running", http.StatusConflict)
			return
		}
		trigger := triggerOf(r)
		go s.autoUpdate(trigger, time.Now(), "")
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch r.Method {
	case http.MethodGet:
		saved := loadSettings(s.fjordRoot)
		start, end := autoWindowOf(saved)
		_, opened := windowAt(time.Now(), start, end)
		s.auto.mu.Lock()
		last, running, pausedAt := s.auto.f.Last, s.auto.running, s.auto.f.PausedAt
		s.auto.mu.Unlock()
		next := opened
		if !last.Window.Before(opened) { // this window's pass is done
			_, next = windowAt(opened.Add(24*time.Hour), start, end)
		}
		stacksOn, stacksTotal := s.autoUpdateCounts()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"start":         start,
			"end":           end,
			"defaultForNew": saved.AutoUpdateDefault,
			"stacksOn":      stacksOn,
			"stacksTotal":   stacksTotal,
			"running":       running,
			"next":          rfc3339OrEmpty(next),
			"last":          last,
			"paused":        rfc3339OrEmpty(pausedAt),
			"offer":         s.unasked(),
		})
	case http.MethodPost:
		var req struct {
			Start         string `json:"start"`
			End           string `json:"end"`
			DefaultForNew *bool  `json:"defaultForNew"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", 400)
			return
		}
		hasWindow := req.Start != "" || req.End != ""
		if !hasWindow && req.DefaultForNew == nil {
			http.Error(w, "start/end or defaultForNew is required", 400)
			return
		}
		if hasWindow {
			s0, ok1 := parseHM(req.Start)
			e0, ok2 := parseHM(req.End)
			if !ok1 || !ok2 || s0 == e0 {
				http.Error(w, "start and end are times like 03:00, and not the same", 400)
				return
			}
		}
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) {
			if hasWindow {
				st.AutoUpdateStart, st.AutoUpdateEnd = req.Start, req.End
			}
			if req.DefaultForNew != nil {
				st.AutoUpdateDefault = *req.DefaultForNew
			}
		}); err != nil {
			http.Error(w, "persist: "+err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// unasked is the stacks no one has said Keep up to date on or off for:
// those installed before it existed. 0 once the offer is answered.
func (s *server) unasked() []string {
	if loadSettings(s.fjordRoot).AutoUpdateOffered {
		return nil
	}
	stacks, err := s.manager.List()
	if err != nil {
		return nil
	}
	var out []string
	for _, st := range stacks {
		if state, _ := s.manager.LoadState(st.Name); state != nil && state.UpdatePolicy == "" && state.DesiredState != "deleting" {
			out = append(out, st.Name)
		}
	}
	return out
}

// autoUpdateOffer answers the offer made once to existing stacks: POST
// .../offer {"answer": "all" | "no"}. All turns Keep up to date on for every
// stack not asked yet; either way the offer is not made again. A stack can
// still be turned on or off in its own Settings tab.
func (s *server) autoUpdateOffer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Answer != "all" && req.Answer != "no") {
		http.Error(w, `send {"answer": "all" | "no"}`, 400)
		return
	}
	var turned []string
	if req.Answer == "all" {
		for _, name := range s.unasked() {
			if err := s.manager.SetUpdatePolicy(name, string(updates.Minors), nil); err != nil {
				http.Error(w, name+": "+err.Error(), 500)
				return
			}
			turned = append(turned, name)
		}
	}
	if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.AutoUpdateOffered = true }); err != nil {
		http.Error(w, "persist: "+err.Error(), 500)
		return
	}
	log.Printf("auto-update: offer answered %s (%d stacks on)", req.Answer, len(turned))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"on": turned})
}

func policyIsOn(raw string) bool {
	return raw != "" && raw != string(updates.Off) && raw != string(updates.Notify)
}

func (s *server) autoUpdateCounts() (on, total int) {
	stacks, err := s.manager.List()
	if err != nil {
		return 0, 0
	}
	for _, st := range stacks {
		state, _ := s.manager.LoadState(st.Name)
		if state != nil && state.DesiredState == "deleting" {
			continue
		}
		total++
		if state != nil && policyIsOn(state.UpdatePolicy) {
			on++
		}
	}
	return on, total
}

// autoUpdateAll sets Keep up to date on or off for every existing stack:
// POST /api/autoupdate/all {"on": true | false}. Individual stacks can still
// be changed afterwards in their Settings tab.
func (s *server) autoUpdateAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		On *bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.On == nil {
		http.Error(w, `send {"on": true | false}`, 400)
		return
	}
	policy := string(updates.Off)
	if *req.On {
		policy = string(updates.Minors)
	}
	stacks, err := s.manager.List()
	if err != nil {
		http.Error(w, "list stacks: "+err.Error(), 500)
		return
	}
	updated := 0
	for _, st := range stacks {
		state, _ := s.manager.LoadState(st.Name)
		if state != nil && state.DesiredState == "deleting" {
			continue
		}
		cur := ""
		if state != nil {
			cur = state.UpdatePolicy
		}
		if *req.On && policyIsOn(cur) {
			continue
		}
		if !*req.On && cur == string(updates.Off) {
			continue
		}
		var svcPol map[string]string
		if state != nil {
			svcPol = state.ServicePolicy
		}
		if err := s.manager.SetUpdatePolicy(st.Name, policy, svcPol); err != nil {
			http.Error(w, st.Name+": "+err.Error(), 500)
			return
		}
		updated++
	}
	if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.AutoUpdateOffered = true }); err != nil {
		http.Error(w, "persist: "+err.Error(), 500)
		return
	}
	on, total := s.autoUpdateCounts()
	log.Printf("auto-update: set all %v (%d updated, %d/%d on)", *req.On, updated, on, total)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"updated":     updated,
		"stacksOn":    on,
		"stacksTotal": total,
	})
}
