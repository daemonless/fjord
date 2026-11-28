package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// bgJobItem is one app/step inside a daemon-owned background job.
type bgJobItem struct {
	Name   string `json:"name"`
	Status string `json:"status"` // queued | running | done | skipped | failed
	Detail string `json:"detail,omitempty"`
}

// bgJob is the live status of a daemon-owned background operation shown in the
// global status strip across all pages.
type bgJob struct {
	ID          string      `json:"id"`
	Kind        string      `json:"kind"` // migrate-all | migrate-dataset | update-all | auto-update | restore-points
	Title       string      `json:"title"`
	Status      string      `json:"status"` // running | done | failed | stopped
	Stopping    bool        `json:"stopping,omitempty"`
	Cancellable bool        `json:"cancellable,omitempty"`
	StartedAt   time.Time   `json:"startedAt"`
	FinishedAt  *time.Time  `json:"finishedAt,omitempty"`
	Total       int         `json:"total"`
	Done        int         `json:"done"`
	Current     string      `json:"current,omitempty"`
	Phase       string      `json:"phase,omitempty"`
	BytesDone   int64       `json:"bytesDone,omitempty"`
	BytesTotal  int64       `json:"bytesTotal,omitempty"`
	Summary     string      `json:"summary,omitempty"`
	Items       []bgJobItem `json:"items,omitempty"`
}

// jobHub tracks active and undismissed completed background jobs and pushes
// changes over the SSE eventHub so every open browser stays in sync with zero
// polling.
type jobHub struct {
	mu             sync.Mutex
	seq            int64
	order          []string
	jobs           map[string]*bgJob
	lastBytePush   map[string]time.Time
	events         *eventHub
	bytePushWindow time.Duration
}

func newJobHub(events *eventHub) *jobHub {
	return &jobHub{
		jobs:           map[string]*bgJob{},
		lastBytePush:   map[string]time.Time{},
		events:         events,
		bytePushWindow: 250 * time.Millisecond,
	}
}

func (h *jobHub) start(kind, title string, cancellable bool, phase string, items []bgJobItem) string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	// Clear any older finished job of the same kind so banners don't pile up.
	kept := h.order[:0]
	for _, id := range h.order {
		j := h.jobs[id]
		if j != nil && j.Kind == kind && j.Status != "running" {
			delete(h.jobs, id)
			delete(h.lastBytePush, id)
			continue
		}
		kept = append(kept, id)
	}
	h.order = kept

	h.seq++
	id := fmt.Sprintf("%s-%d", kind, h.seq)
	copied := make([]bgJobItem, len(items))
	copy(copied, items)
	h.jobs[id] = &bgJob{
		ID:          id,
		Kind:        kind,
		Title:       title,
		Status:      "running",
		Cancellable: cancellable,
		StartedAt:   time.Now().UTC(),
		Total:       len(copied),
		Phase:       phase,
		Items:       copied,
	}
	h.order = append(h.order, id)
	snap := h.listLocked()
	h.mu.Unlock()
	h.broadcast(snap)
	return id
}

func (h *jobHub) setItems(id string, items []bgJobItem) {
	if h == nil || id == "" {
		return
	}
	h.mu.Lock()
	j := h.jobs[id]
	if j == nil {
		h.mu.Unlock()
		return
	}
	copied := make([]bgJobItem, len(items))
	copy(copied, items)
	j.Items = copied
	j.Total = len(copied)
	snap := h.listLocked()
	h.mu.Unlock()
	h.broadcast(snap)
}

func (h *jobHub) setPhase(id, current, phase string, bytesDone, bytesTotal int64) {
	if h == nil || id == "" {
		return
	}
	h.mu.Lock()
	j := h.jobs[id]
	if j == nil || j.Status != "running" {
		h.mu.Unlock()
		return
	}
	structural := j.Current != current || j.Phase != phase || j.BytesTotal != bytesTotal
	j.Current = current
	j.Phase = phase
	j.BytesDone = bytesDone
	j.BytesTotal = bytesTotal
	if current != "" {
		found := false
		for i := range j.Items {
			if j.Items[i].Name == current {
				if j.Items[i].Status == "queued" {
					j.Items[i].Status = "running"
					structural = true
				}
				found = true
				break
			}
		}
		if !found {
			j.Items = append(j.Items, bgJobItem{Name: current, Status: "running"})
			if len(j.Items) > j.Total {
				j.Total = len(j.Items)
			}
			structural = true
		}
	}
	now := time.Now()
	shouldPush := structural
	if !shouldPush {
		if last, ok := h.lastBytePush[id]; !ok || now.Sub(last) >= h.bytePushWindow {
			shouldPush = true
		}
	}
	if !shouldPush {
		h.mu.Unlock()
		return
	}
	h.lastBytePush[id] = now
	snap := h.listLocked()
	h.mu.Unlock()
	h.broadcast(snap)
}

func (h *jobHub) recordItem(id, name, status, detail string) {
	if h == nil || id == "" {
		return
	}
	h.mu.Lock()
	j := h.jobs[id]
	if j == nil {
		h.mu.Unlock()
		return
	}
	found := false
	for i := range j.Items {
		if j.Items[i].Name == name {
			prev := j.Items[i].Status
			j.Items[i].Status = status
			if detail != "" {
				j.Items[i].Detail = detail
			}
			if prev == "queued" || prev == "running" {
				j.Done++
			}
			found = true
			break
		}
	}
	if !found {
		j.Items = append(j.Items, bgJobItem{Name: name, Status: status, Detail: detail})
		j.Done++
		if len(j.Items) > j.Total {
			j.Total = len(j.Items)
		}
	}
	if j.Current == name {
		j.BytesDone = 0
		j.BytesTotal = 0
	}
	snap := h.listLocked()
	h.mu.Unlock()
	h.broadcast(snap)
}

func (h *jobHub) finish(id, status, summary string) {
	if h == nil || id == "" {
		return
	}
	h.mu.Lock()
	j := h.jobs[id]
	if j == nil {
		h.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	j.Status = status
	j.Stopping = false
	j.FinishedAt = &now
	j.Current = ""
	j.Phase = ""
	j.BytesDone = 0
	j.BytesTotal = 0
	j.Summary = summary
	delete(h.lastBytePush, id)
	snap := h.listLocked()
	h.mu.Unlock()
	h.broadcast(snap)
}

// remove deletes a job immediately (used when an automatic scheduled pass had
// nothing to report so idle hosts stay quiet).
func (h *jobHub) remove(id string) {
	if h == nil || id == "" {
		return
	}
	h.mu.Lock()
	if _, ok := h.jobs[id]; !ok {
		h.mu.Unlock()
		return
	}
	delete(h.jobs, id)
	delete(h.lastBytePush, id)
	kept := h.order[:0]
	for _, oid := range h.order {
		if oid != id {
			kept = append(kept, oid)
		}
	}
	h.order = kept
	snap := h.listLocked()
	h.mu.Unlock()
	h.broadcast(snap)
}

func (h *jobHub) cancel(id string) (kind string, ok bool) {
	if h == nil || id == "" {
		return "", false
	}
	h.mu.Lock()
	j := h.jobs[id]
	if j == nil || j.Status != "running" || !j.Cancellable {
		h.mu.Unlock()
		return "", false
	}
	j.Stopping = true
	kind = j.Kind
	snap := h.listLocked()
	h.mu.Unlock()
	h.broadcast(snap)
	return kind, true
}

func (h *jobHub) isStopping(id string) bool {
	if h == nil || id == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	j := h.jobs[id]
	return j != nil && j.Stopping
}

func (h *jobHub) dismiss(id string) (found, running bool) {
	if h == nil || id == "" {
		return false, false
	}
	h.mu.Lock()
	j := h.jobs[id]
	if j == nil {
		h.mu.Unlock()
		return false, false
	}
	if j.Status == "running" {
		h.mu.Unlock()
		return true, true
	}
	delete(h.jobs, id)
	delete(h.lastBytePush, id)
	kept := h.order[:0]
	for _, oid := range h.order {
		if oid != id {
			kept = append(kept, oid)
		}
	}
	h.order = kept
	snap := h.listLocked()
	h.mu.Unlock()
	h.broadcast(snap)
	return true, false
}

func (h *jobHub) list() []bgJob {
	if h == nil {
		return []bgJob{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.listLocked()
}

func (h *jobHub) listLocked() []bgJob {
	out := make([]bgJob, 0, len(h.order))
	for _, id := range h.order {
		j := h.jobs[id]
		if j == nil {
			continue
		}
		cp := *j
		if j.FinishedAt != nil {
			t := *j.FinishedAt
			cp.FinishedAt = &t
		}
		if len(j.Items) > 0 {
			cp.Items = make([]bgJobItem, len(j.Items))
			copy(cp.Items, j.Items)
		}
		out = append(out, cp)
	}
	return out
}

func (h *jobHub) broadcast(jobs []bgJob) {
	if h == nil || h.events == nil || h.events.count() == 0 {
		return
	}
	if b, err := json.Marshal(map[string]any{
		"type": "jobs",
		"jobs": jobs,
	}); err == nil {
		h.events.publish(b)
	}
}

func (s *server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"jobs": s.jobs.list()})
}

func (s *server) handleJobByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	id, action, ok := strings.Cut(rest, "/")
	if !ok || id == "" {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "cancel":
		kind, cancelled := s.jobs.cancel(id)
		if !cancelled {
			http.Error(w, "job is not running or cannot be stopped", http.StatusConflict)
			return
		}
		if kind == "migrate-all" {
			s.migrateAll.cancel()
		}
		w.WriteHeader(http.StatusNoContent)
	case "dismiss":
		found, running := s.jobs.dismiss(id)
		if !found {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		if running {
			http.Error(w, "cannot dismiss a running job", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}
