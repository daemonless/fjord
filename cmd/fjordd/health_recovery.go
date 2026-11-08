package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

const (
	// restartLoopWindow is how long container restart jumps are remembered:
	// two or more restarts inside this window mark a running container as
	// crash-looping even when polled between crashes.
	restartLoopWindow = 5 * time.Minute
	// healthyResetAfter is how long a recovered stack must stay running cleanly
	// before its recovery attempt counter resets to zero.
	healthyResetAfter = 2 * time.Minute
)

// recoveryView is what the stack page reads for a stack's recovery policy and
// recent recovery attempts.
type recoveryView struct {
	Action    string                `json:"action"` // "none" | "restart" | "stop"
	MaxTries  int                   `json:"maxTries"`
	Cooldown  int                   `json:"cooldown"` // seconds
	Attempts  int                   `json:"attempts,omitempty"`
	Exhausted bool                  `json:"exhausted,omitempty"`
	Events    []stack.RecoveryEvent `json:"events,omitempty"`
}

type containerRestarts struct {
	id    string
	last  int
	jumps []time.Time
}

type stackHealthState struct {
	unhealthyChecks int
	healthySince    time.Time
	reportedWhy     string
	attempts        int
	lastAttempt     time.Time
	exhausted       bool
}

type healthMonitor struct {
	mu         sync.Mutex
	containers map[string]*containerRestarts // "<stack>/<container>" -> restart tracking
	stacks     map[string]*stackHealthState  // "<stack>" -> episode state
	// run executes a recovery action (async in production, sync in unit tests).
	run func(fn func())
}

func newHealthMonitor() *healthMonitor {
	return &healthMonitor{
		containers: map[string]*containerRestarts{},
		stacks:     map[string]*stackHealthState{},
		run:        func(fn func()) { go fn() },
	}
}

func (h *healthMonitor) ensure() {
	if h.containers == nil {
		h.containers = map[string]*containerRestarts{}
	}
	if h.stacks == nil {
		h.stacks = map[string]*stackHealthState{}
	}
	if h.run == nil {
		h.run = func(fn func()) { go fn() }
	}
}

// reset clears a stack's recovery episode state (after a manual lifecycle
// action, a policy change, or a delete).
func (h *healthMonitor) reset(name string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.stacks, name)
	prefix := name + "/"
	for k := range h.containers {
		if strings.HasPrefix(k, prefix) {
			delete(h.containers, k)
		}
	}
}

// observeContainers tracks container RestartCount jumps across polls and sets
// Detail on a container that is crash-looping between crashes (restart: always
// or unless-stopped reads "running" between exits).
func (h *healthMonitor) observeContainers(stackName string, status *engine.StackStatus, now time.Time) {
	if h == nil || status == nil || !status.StaleSince.IsZero() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ensure()

	for i := range status.Containers {
		c := &status.Containers[i]
		key := stackName + "/" + c.Name
		cr, ok := h.containers[key]
		if !ok || (c.ID != "" && cr.id != "" && cr.id != c.ID) {
			h.containers[key] = &containerRestarts{id: c.ID, last: c.Restarts}
			continue
		}
		if c.ID != "" {
			cr.id = c.ID
		}
		if c.Restarts > cr.last {
			delta := c.Restarts - cr.last
			for range min(delta, 10) {
				cr.jumps = append(cr.jumps, now)
			}
			cr.last = c.Restarts
		} else if c.Restarts < cr.last {
			cr.last = c.Restarts
			cr.jumps = nil
		}
		kept := cr.jumps[:0]
		for _, t := range cr.jumps {
			if now.Sub(t) <= restartLoopWindow {
				kept = append(kept, t)
			}
		}
		cr.jumps = kept
		if len(cr.jumps) >= 2 && c.State == "running" && c.Detail == "" {
			c.Detail = fmt.Sprintf("crash-looping: restarted %d times", c.Restarts)
		}
	}
}

// normalizeRecovery validates and fills defaults on a RecoveryPolicy.
func normalizeRecovery(r stack.RecoveryPolicy) (stack.RecoveryPolicy, error) {
	if r.Action == "" {
		r.Action = stack.RecoveryNone
	}
	switch r.Action {
	case stack.RecoveryNone, stack.RecoveryRestart, stack.RecoveryStop:
	default:
		return r, fmt.Errorf("unknown recovery action %q: use none, restart, or stop", r.Action)
	}
	if r.MaxTries <= 0 {
		r.MaxTries = stack.DefaultRecoveryMaxTries
	} else if r.MaxTries > 20 {
		r.MaxTries = 20
	}
	if r.Cooldown <= 0 {
		r.Cooldown = stack.DefaultRecoveryCooldown
	} else if r.Cooldown < 10 {
		r.Cooldown = 10
	} else if r.Cooldown > 3600 {
		r.Cooldown = 3600
	}
	return r, nil
}

func recoveryPolicyOf(st *stack.Stack) stack.RecoveryPolicy {
	if st != nil && st.State != nil && st.State.Recovery != nil {
		if p, err := normalizeRecovery(*st.State.Recovery); err == nil {
			return p
		}
	}
	return stack.RecoveryPolicy{
		Action:   stack.RecoveryNone,
		MaxTries: stack.DefaultRecoveryMaxTries,
		Cooldown: stack.DefaultRecoveryCooldown,
	}
}

func (s *server) recoveryViewOf(st *stack.Stack) recoveryView {
	p := recoveryPolicyOf(st)
	v := recoveryView{
		Action:   p.Action,
		MaxTries: p.MaxTries,
		Cooldown: p.Cooldown,
	}
	if st != nil && st.State != nil && len(st.State.RecoveryEvents) > 0 {
		v.Events = st.State.RecoveryEvents
	}
	if s.health != nil && st != nil {
		s.health.mu.Lock()
		if hs, ok := s.health.stacks[st.Name]; ok {
			v.Attempts = hs.attempts
			v.Exhausted = hs.exhausted
		}
		s.health.mu.Unlock()
	}
	return v
}

// stackRecovery sets a stack's recovery policy: POST /api/stacks/<name>/recovery-policy
// {"action": "restart", "maxTries": 3, "cooldown": 60}.
func (s *server) stackRecovery(w http.ResponseWriter, r *http.Request, name string) {
	var req stack.RecoveryPolicy
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	policy, err := normalizeRecovery(req)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if _, err := s.manager.Get(name); err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	if err := s.manager.UpdateState(name, func(st *stack.State) *stack.State {
		if st == nil {
			st = &stack.State{}
		}
		if policy.Action == stack.RecoveryNone &&
			policy.MaxTries == stack.DefaultRecoveryMaxTries &&
			policy.Cooldown == stack.DefaultRecoveryCooldown {
			st.Recovery = nil
		} else {
			cp := policy
			st.Recovery = &cp
		}
		return st
	}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if s.health != nil {
		s.health.reset(name)
	}
	log.Printf("%s: recovery policy %s (maxTries=%d, cooldown=%ds)", name, policy.Action, policy.MaxTries, policy.Cooldown)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "saved"})
}

// restartCountRe matches dynamic restart count fragments inside health Detail
// strings so a continuing crash loop on the same container deduplicates to one
// event per episode under policy "none".
var restartCountRe = regexp.MustCompile(`\b(?:restarted \d+ times?|after \d+ restarts?|\d+ restarts?)\b`)

func normalizeHealthReason(why string) string {
	return restartCountRe.ReplaceAllString(why, "<restarts>")
}

// isOneShotCleanExit reports whether a container belongs to a one-shot
// service ("no" beside others that restart, matching compose.RestartOf) and
// has actually exited cleanly with status 0.
func isOneShotCleanExit(restarts map[string]string, svc string, c engine.ContainerStatus) bool {
	if c.State != "exited" || c.ExitCode != 0 || c.Detail != "" {
		return false
	}
	if restarts[svc] != composepkg.RestartNo && restarts[c.Name] != composepkg.RestartNo {
		return false
	}
	for _, v := range restarts {
		if v != composepkg.RestartNo {
			return true
		}
	}
	return false
}

func stillStarting(status engine.StackStatus) bool {
	for _, c := range status.Containers {
		if c.State == "starting" {
			return true
		}
	}
	return status.State != "running" && len(status.Containers) == 0
}

// unhealthyReason inspects a stack's live status and returns why it failed its
// health check ("" when healthy or still starting), plus whether the failure is
// already confirmed (a crash loop or unhealthy probe vs a container that just
// read stopped once).
func unhealthyReason(st *stack.Stack, status engine.StackStatus) (why string, confirmed bool) {
	var restarts map[string]string
	if st != nil && st.Compose != "" {
		restarts = composepkg.ServiceRestarts(st.Compose)
	}
	n := len(status.Containers)
	for _, c := range status.Containers {
		svc := c.Service
		if svc == "" {
			svc = c.Name
		}
		prefix := func(msg string) string {
			if n > 1 && svc != "" {
				return svc + ": " + msg
			}
			return msg
		}
		if c.State == "crashed" {
			detail := c.Detail
			if detail == "" {
				detail = "the app keeps crashing -- see Logs"
			}
			return prefix(detail), true
		}
		if strings.HasPrefix(c.Detail, "crash-looping") {
			return prefix(c.Detail), true
		}
	}
	for _, c := range status.Containers {
		if c.State == "running" || c.State == "starting" {
			continue
		}
		svc := c.Service
		if svc == "" {
			svc = c.Name
		}
		if isOneShotCleanExit(restarts, svc, c) {
			continue
		}
		detail := c.Detail
		if detail == "" {
			detail = "stopped unexpectedly (" + c.State + ")"
		}
		if n > 1 && svc != "" {
			return svc + ": " + detail, false
		}
		return detail, false
	}
	if status.State == "stopped" && len(status.Containers) == 0 {
		return "stopped unexpectedly", false
	}
	return "", false
}

// observeStackHealth evaluates one stack's health during the background loop
// and runs its configured recovery policy when a check fails.
func (s *server) observeStackHealth(st *stack.Stack, status engine.StackStatus, now time.Time) {
	if s.health == nil || st == nil || st.State == nil || st.State.DesiredState != "running" {
		return
	}
	if s.booting.Load() || loadBusy(st.Name) != nil {
		return
	}
	if status.State == "unknown" || !status.StaleSince.IsZero() {
		return
	}

	why, confirmed := unhealthyReason(st, status)

	s.health.mu.Lock()
	s.health.ensure()
	hs, ok := s.health.stacks[st.Name]
	if !ok {
		hs = &stackHealthState{}
		s.health.stacks[st.Name] = hs
	}

	if why == "" {
		if stillStarting(status) {
			// Still starting: wait without resetting or failing.
			s.health.mu.Unlock()
			return
		}
		hs.unhealthyChecks = 0
		hs.reportedWhy = ""
		if hs.healthySince.IsZero() {
			hs.healthySince = now
		}
		if (hs.attempts > 0 || hs.exhausted) && now.Sub(hs.healthySince) >= healthyResetAfter {
			hs.attempts = 0
			hs.exhausted = false
		}
		s.health.mu.Unlock()

		if st.State.LastFailure != nil && st.State.LastFailure.Action == "health" {
			_ = s.manager.UpdateState(st.Name, func(state *stack.State) *stack.State {
				if state == nil || state.LastFailure == nil || state.LastFailure.Action != "health" {
					return nil
				}
				state.LastFailure = nil
				return state
			})
		}
		return
	}

	hs.healthySince = time.Time{}
	hs.unhealthyChecks++
	if !confirmed && hs.unhealthyChecks < 2 {
		s.health.mu.Unlock()
		return
	}

	policy := recoveryPolicyOf(st)
	run := s.health.run
	normWhy := normalizeHealthReason(why)

	switch policy.Action {
	case stack.RecoveryNone:
		if hs.reportedWhy == normWhy {
			s.health.mu.Unlock()
			return
		}
		hs.reportedWhy = normWhy
		s.health.mu.Unlock()

		log.Printf("health %s: %s (recovery: none)", st.Name, why)
		s.recordRecoveryState(st.Name, &stack.Failure{
			Action:  "health",
			At:      now,
			Message: why,
		}, stack.RecoveryEvent{
			At:      now,
			Check:   why,
			Action:  stack.RecoveryNone,
			Outcome: "detected",
			Message: why,
		})

	case stack.RecoveryStop:
		if hs.attempts > 0 {
			s.health.mu.Unlock()
			return
		}
		// Try Down once per unhealthy episode; if Down fails, the failure is
		// recorded once rather than retrying Down on every check tick.
		prevAttempt := hs.lastAttempt
		hs.attempts = 1
		hs.lastAttempt = now
		hs.reportedWhy = normWhy
		s.health.mu.Unlock()

		run(func() {
			s.recoverStop(st.Name, why, now, prevAttempt)
		})

	case stack.RecoveryRestart:
		if hs.attempts >= policy.MaxTries {
			if hs.exhausted {
				s.health.mu.Unlock()
				return
			}
			hs.exhausted = true
			attempts := hs.attempts
			s.health.mu.Unlock()

			msg := fmt.Sprintf("%s — recovery gave up after %d restart%s",
				why, policy.MaxTries, map[bool]string{true: "", false: "s"}[policy.MaxTries == 1])
			log.Printf("health %s: %s", st.Name, msg)
			s.recordRecoveryState(st.Name, &stack.Failure{
				Action:  "health",
				At:      now,
				Message: msg,
			}, stack.RecoveryEvent{
				At:      now,
				Check:   why,
				Action:  stack.RecoveryRestart,
				Attempt: attempts,
				Max:     policy.MaxTries,
				Outcome: "exhausted",
				Message: msg,
			})
			return
		}
		if !hs.lastAttempt.IsZero() && now.Sub(hs.lastAttempt) < time.Duration(policy.Cooldown)*time.Second {
			s.health.mu.Unlock()
			return
		}
		prevAttempt := hs.lastAttempt
		hs.attempts++
		attempt := hs.attempts
		hs.lastAttempt = now
		hs.reportedWhy = normWhy
		s.health.mu.Unlock()

		run(func() {
			s.recoverRestart(st.Name, why, attempt, policy.MaxTries, now, prevAttempt)
		})
	default:
		s.health.mu.Unlock()
	}
}

func (s *server) recordRecoveryState(name string, failure *stack.Failure, ev stack.RecoveryEvent) {
	err := s.manager.UpdateState(name, func(st *stack.State) *stack.State {
		if st == nil {
			return nil
		}
		if failure != nil {
			st.LastFailure = failure
		}
		st.RecoveryEvents = append(st.RecoveryEvents, ev)
		if extra := len(st.RecoveryEvents) - stack.MaxRecoveryEvents; extra > 0 {
			st.RecoveryEvents = append([]stack.RecoveryEvent(nil), st.RecoveryEvents[extra:]...)
		}
		return st
	})
	if err != nil {
		log.Printf("health %s: persist recovery state: %v", name, err)
	}
}

// cancelRecoveryAttempt refunds an attempt and restores lastAttempt when
// recovery could not take the stack lock or found the stack stopped, busy,
// or healthy under the lock.
func (s *server) cancelRecoveryAttempt(name string, prevAttempt time.Time) {
	if s.health == nil {
		return
	}
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	if hs, ok := s.health.stacks[name]; ok && hs.attempts > 0 {
		hs.attempts--
		hs.lastAttempt = prevAttempt
	}
}

// lockStackForRecovery acquires the stack lock without taking over a user's
// stay-up watch, records the stack in busyOps while holding the lock, and
// re-checks with a bounded statusWait timeout that the stack still wants to
// run and is still unhealthy before publishing busy to open pages.
func (s *server) lockStackForRecovery(ctx context.Context, name, action string, now time.Time) (*stack.Stack, func(), bool) {
	if loadBusy(name) != nil {
		return nil, nil, false
	}
	u, ok := lockStack(name)
	if !ok {
		return nil, nil, false
	}
	if loadBusy(name) != nil {
		u()
		return nil, nil, false
	}
	busyOps.Store(name, &busyOp{action: action, since: time.Now()})
	abort := func() {
		busyOps.Delete(name)
		u()
	}

	st, err := s.manager.Get(name)
	if err != nil || st.State == nil || st.State.DesiredState != "running" {
		abort()
		return nil, nil, false
	}
	sctx, cancel := context.WithTimeout(ctx, statusWait)
	status, err := s.backendFor(st).Status(sctx, st)
	cancel()
	if err != nil || status.State == "unknown" || !status.StaleSince.IsZero() {
		abort()
		return nil, nil, false
	}
	s.health.observeContainers(name, &status, now)
	if why, _ := unhealthyReason(st, status); why == "" {
		abort()
		return nil, nil, false
	}
	outputs.start(name, action)
	publishBusy(name, action)
	return st, func() {
		outputs.finish(name)
		busyOps.Delete(name)
		publishBusy(name, "")
		u()
	}, true
}

func (s *server) recoverStop(name, why string, now, prevAttempt time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	st, unlock, ok := s.lockStackForRecovery(ctx, name, "down", now)
	if !ok {
		s.cancelRecoveryAttempt(name, prevAttempt)
		return
	}
	defer unlock()

	log.Printf("health %s: %s — stopping stack per recovery policy", name, why)
	header := fmt.Sprintf("[fjord] health check failed (%s); stopping stack per recovery policy\n", why)
	kept := outputs.current(name, "down")
	_, _ = io.WriteString(kept, header)
	watch := &errorWatch{}
	stream, err := s.backendFor(st).Down(ctx, st)
	if err != nil {
		_, _ = io.WriteString(io.MultiWriter(kept, watch), "[error] "+err.Error()+"\n")
	} else {
		_, _ = io.Copy(io.MultiWriter(kept, watch), stream)
		stream.Close()
	}
	kept.finish()

	outcome := "stopped"
	msg := why + " — stopped by recovery policy"
	if err != nil || watch.seen {
		outcome = "failed"
		errMsg := watch.Message()
		if errMsg == "" && err != nil {
			errMsg = err.Error()
		}
		msg = fmt.Sprintf("%s — recovery stop failed: %s", why, errMsg)
	} else {
		_ = s.manager.SetDesiredState(name, "stopped")
	}
	s.recordRecoveryState(name, &stack.Failure{
		Action:  "health",
		At:      now,
		Message: msg,
	}, stack.RecoveryEvent{
		At:      now,
		Check:   why,
		Action:  stack.RecoveryStop,
		Attempt: 1,
		Outcome: outcome,
		Message: msg,
	})
}

func (s *server) recoverRestart(name, why string, attempt, maxTries int, now, prevAttempt time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	st, unlock, ok := s.lockStackForRecovery(ctx, name, "restart", now)
	if !ok {
		s.cancelRecoveryAttempt(name, prevAttempt)
		return
	}
	defer unlock()

	log.Printf("health %s: %s — recovery restart %d/%d", name, why, attempt, maxTries)
	header := fmt.Sprintf("[fjord] health check failed (%s) — recovery restart %d/%d\n", why, attempt, maxTries)
	kept := outputs.current(name, "restart")
	_, _ = io.WriteString(kept, header)
	watch := &errorWatch{}
	stream, err := s.backendFor(st).Restart(ctx, st)
	if err != nil {
		_, _ = io.WriteString(io.MultiWriter(kept, watch), "[error] "+err.Error()+"\n")
	} else {
		_, _ = io.Copy(io.MultiWriter(kept, watch), s.keepAfter(ctx, st, stream))
		stream.Close()
	}
	kept.finish()

	outcome := "restarted"
	msg := fmt.Sprintf("%s — recovery restart %d/%d", why, attempt, maxTries)
	if watch.seen {
		outcome = "failed"
		msg = fmt.Sprintf("%s — recovery restart %d/%d failed: %s", why, attempt, maxTries, watch.Message())
	}
	s.recordRecoveryState(name, &stack.Failure{
		Action:  "health",
		At:      now,
		Message: msg,
	}, stack.RecoveryEvent{
		At:      now,
		Check:   why,
		Action:  stack.RecoveryRestart,
		Attempt: attempt,
		Max:     maxTries,
		Outcome: outcome,
		Message: msg,
	})
}
