package main

import (
	"cmp"
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/daemonless/fjord/pkg/history"
	"github.com/daemonless/fjord/pkg/stack"
)

// recordFailureNotice opens or increments an unattended failure notice on name.
// Returns true when this failure starts a new notice (count == 1) and false
// when it increments an existing ongoing notice.
func (s *server) recordFailureNotice(name, kind, message string, at time.Time) bool {
	msg := history.FirstLine(message)
	started := false
	if err := s.manager.UpdateState(name, func(st *stack.State) *stack.State {
		if st == nil {
			return nil
		}
		started = st.RecordFailureNotice(kind, msg, at)
		return st
	}); err != nil {
		log.Printf("notice %s (%s): record: %v", name, kind, err)
	}
	return started
}

// clearFailureNotice deletes the notice for (name, kind) on success and
// returns the cleared notice, or nil if none was active.
func (s *server) clearFailureNotice(name, kind string) *stack.FailureNotice {
	var cleared *stack.FailureNotice
	if err := s.manager.UpdateState(name, func(st *stack.State) *stack.State {
		if st == nil || !st.HasFailureNotice(kind) {
			return nil
		}
		cleared = st.ClearFailureNotice(kind)
		return st
	}); err != nil {
		log.Printf("notice %s (%s): clear: %v", name, kind, err)
	}
	return cleared
}

// hasFailureNotice reports whether (name, kind) currently has an active notice.
func (s *server) hasFailureNotice(name, kind string) bool {
	st, err := s.manager.LoadState(name)
	return err == nil && st.HasFailureNotice(kind)
}

type fleetNotice struct {
	Stack       string    `json:"stack"`
	DisplayName string    `json:"displayName,omitempty"`
	Kind        string    `json:"kind"`
	Count       int       `json:"count"`
	FirstAt     time.Time `json:"firstAt"`
	LastAt      time.Time `json:"lastAt"`
	Message     string    `json:"message,omitempty"`
	Dismissed   bool      `json:"dismissed,omitempty"`
}

// handleNotices serves GET /api/notices: every active failure notice across all
// non-deleting stacks, including dismissed ones (used by Settings -> Maintenance
// and available for future notification channels).
func (s *server) handleNotices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stacks, err := s.manager.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	notices := []fleetNotice{}
	for _, st := range stacks {
		state, _ := s.manager.LoadState(st.Name)
		if state == nil || state.DesiredState == "deleting" || len(state.FailureNotices) == 0 {
			continue
		}
		for _, n := range state.FailureNotices {
			notices = append(notices, fleetNotice{
				Stack:       st.Name,
				DisplayName: state.DisplayName,
				Kind:        n.Kind,
				Count:       n.Count,
				FirstAt:     n.FirstAt,
				LastAt:      n.LastAt,
				Message:     n.Message,
				Dismissed:   n.Dismissed,
			})
		}
	}
	slices.SortFunc(notices, func(a, b fleetNotice) int {
		if c := b.LastAt.Compare(a.LastAt); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Stack, b.Stack); c != 0 {
			return c
		}
		return cmp.Compare(a.Kind, b.Kind)
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"notices": notices})
}

// stackDismissNotice serves POST /api/stacks/<name>/notices/dismiss with
// {"kind": "..."} to hide the app banner and sidebar mark while the same
// failure continues.
func (s *server) stackDismissNotice(w http.ResponseWriter, r *http.Request, name string) {
	if _, err := s.manager.Get(name); err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}
	var req struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !stack.ValidNoticeKind(req.Kind) {
		http.Error(w, "valid notice kind required", http.StatusBadRequest)
		return
	}
	if err := s.manager.UpdateState(name, func(st *stack.State) *stack.State {
		if st == nil || !st.DismissFailureNotice(req.Kind) {
			return nil
		}
		return st
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
