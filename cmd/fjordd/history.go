package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/daemonless/fjord/pkg/history"
)

// histNote is what an action's History entry needs that its outcome does
// not tell: what it was, who or what started it, and when.
type histNote struct {
	action            string
	trigger           string
	services          []string
	start             time.Time
	detail            string // said even when it went fine (why recovery acted)
	skipFailedHistory bool   // repeated unattended failure updates its notice instead of spamming History
}

func newNote(action, trigger string, services ...string) *histNote {
	return &histNote{action: action, trigger: trigger, services: services, start: time.Now()}
}

// triggerOf is who started a request's action: the fjord command on this
// host, or someone on the page.
func triggerOf(r *http.Request) string {
	if r != nil && isLocalSocket(r) {
		return history.ByCLI
	}
	return history.ByYou
}

// noteForAction maps a lifecycle action to its History action.
func noteForAction(r *http.Request, action string) *histNote {
	a := map[string]string{"up": history.Start, "down": history.Stop, "restart": history.Restart, "update": history.Update}[action]
	if action == "update" && r != nil && r.Context().Value(isRollbackKey{}) != nil {
		a = history.Rollback
	}
	if a == "" {
		a = action
	}
	return newNote(a, triggerOf(r))
}

// writeHistory adds one entry for name. Best effort: History never fails
// the action it describes.
func (s *server) writeHistory(name string, n *histNote, failed bool, msg string) {
	if s.hist == nil || n == nil {
		return
	}
	if failed && n.skipFailedHistory {
		return
	}
	e := history.Entry{Stack: name, Action: n.action, Trigger: n.trigger, Result: history.OK, Services: n.services}
	if failed {
		e.Result = history.Failed
		e.Message = history.FirstLine(msg)
	} else if n.detail != "" {
		e.Message = history.FirstLine(n.detail)
	}
	if n.action == history.Update || n.action == history.Rollback {
		e.Changes = s.imageChanges(name, n)
	}
	if err := s.hist.Append(e); err != nil {
		log.Printf("%s %s: history: %v", n.action, name, err)
	}
}

// imageChanges is what each service ran before and after an update: before
// from the rollback record the update itself wrote, after from the engine.
func (s *server) imageChanges(name string, n *histNote) []history.Change {
	byService := map[string]*history.Change{}
	var order []string
	change := func(svc string) *history.Change {
		if c, ok := byService[svc]; ok {
			return c
		}
		c := &history.Change{Service: svc}
		byService[svc] = c
		order = append(order, svc)
		return c
	}
	wanted := func(svc string) bool { return len(n.services) == 0 || slices.Contains(n.services, svc) }
	if n.action == history.Update {
		if state, err := s.manager.LoadState(name); err == nil && state != nil {
			for svc, rb := range state.Rollback {
				at, err := time.Parse(time.RFC3339, rb.At)
				if err != nil || at.Before(n.start.Add(-time.Second)) || !wanted(svc) {
					continue
				}
				change(svc).From = &history.Image{Ref: rb.Ref, Digest: rb.Digest}
			}
		}
	}
	if st, err := s.manager.Get(name); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if ris, err := s.backendFor(st).RunningImages(ctx, st); err == nil {
			for _, ri := range ris {
				if wanted(ri.Service) {
					change(ri.Service).To = &history.Image{Ref: ri.Ref, Digest: ri.Digest}
				}
			}
		}
	}
	slices.Sort(order)
	out := make([]history.Change, 0, len(order))
	for _, svc := range order {
		out = append(out, *byService[svc])
	}
	return out
}

// historyLimit reads ?limit=, default 100, at most history.Keep.
func historyLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 || n > history.Keep {
		return 100
	}
	return n
}

// handleHistory is GET /api/history: every stack's entries, deleted stacks
// included, newest first.
func (s *server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeHistoryJSON(w, s.hist, func(l *history.Log) []history.Entry { return l.All(historyLimit(r)) })
}

// stackHistory is GET /api/stacks/<name>/history.
func (s *server) stackHistory(w http.ResponseWriter, r *http.Request, name string) {
	writeHistoryJSON(w, s.hist, func(l *history.Log) []history.Entry { return l.Stack(name, historyLimit(r)) })
}

func writeHistoryJSON(w http.ResponseWriter, l *history.Log, get func(*history.Log) []history.Entry) {
	es := []history.Entry{}
	if l != nil {
		if got := get(l); got != nil {
			es = got
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(es)
}
