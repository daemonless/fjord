package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJobHubLifecycleCancelDismissAndSSE(t *testing.T) {
	ev := newEventHub()
	sub := ev.subscribe()
	defer ev.unsubscribe(sub)

	h := newJobHub(ev)
	id := h.start("migrate-all", "Moving apps to ZFS datasets", true, "Scanning apps", nil)
	if id == "" {
		t.Fatal("expected non-empty job id")
	}

	// Drain initial start event.
	select {
	case msg := <-sub:
		var payload struct {
			Type string  `json:"type"`
			Jobs []bgJob `json:"jobs"`
		}
		if err := json.Unmarshal(msg, &payload); err != nil || payload.Type != "jobs" || len(payload.Jobs) != 1 {
			t.Fatalf("unexpected SSE start payload: %s err=%v", string(msg), err)
		}
	default:
		t.Fatal("expected SSE event on start")
	}

	h.setItems(id, []bgJobItem{
		{Name: "radarr", Status: "queued", Detail: "15.4 GB"},
		{Name: "sonarr", Status: "queued", Detail: "3.4 GB"},
	})
	h.setPhase(id, "radarr", "Copying", 512, 1024)
	if got := h.list(); len(got) != 1 || got[0].Current != "radarr" || got[0].BytesDone != 512 || got[0].Items[0].Status != "running" {
		t.Fatalf("unexpected mid-run state: %+v", got)
	}

	// Cannot dismiss while running.
	if found, running := h.dismiss(id); !found || !running {
		t.Fatalf("dismiss while running: found=%v running=%v", found, running)
	}

	kind, ok := h.cancel(id)
	if !ok || kind != "migrate-all" || !h.isStopping(id) {
		t.Fatalf("cancel failed: kind=%q ok=%v stopping=%v", kind, ok, h.isStopping(id))
	}

	h.recordItem(id, "radarr", "done", "15.4 GB")
	h.recordItem(id, "sonarr", "skipped", "Stopped")
	h.finish(id, "stopped", "Stopped after moving 1 of 2 apps")

	got := h.list()
	if len(got) != 1 || got[0].Status != "stopped" || got[0].Done != 2 || got[0].FinishedAt == nil {
		t.Fatalf("unexpected finished state: %+v", got)
	}

	// Concurrent job of another kind stacks cleanly.
	id2 := h.start("restore-points", "Saving daily restore points", false, "Checking changes", nil)
	if len(h.list()) != 2 {
		t.Fatalf("expected 2 concurrent/visible jobs, got %d", len(h.list()))
	}
	h.remove(id2)
	if len(h.list()) != 1 {
		t.Fatalf("expected remove to leave 1 job, got %d", len(h.list()))
	}

	// Dismiss finished job.
	if found, running := h.dismiss(id); !found || running {
		t.Fatalf("dismiss finished: found=%v running=%v", found, running)
	}
	if len(h.list()) != 0 {
		t.Fatalf("expected empty list after dismiss, got %+v", h.list())
	}
}

func TestJobsHTTPHandlers(t *testing.T) {
	s := &server{jobs: newJobHub(nil)}
	id := s.jobs.start("update-all", "Updating apps", true, "Updating", []bgJobItem{{Name: "seerr", Status: "queued"}})

	// GET /api/jobs
	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	rec := httptest.NewRecorder()
	s.handleJobs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/jobs status=%d", rec.Code)
	}
	var resp struct {
		Jobs []bgJob `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Jobs) != 1 || resp.Jobs[0].ID != id {
		t.Fatalf("unexpected GET /api/jobs body: %s", rec.Body.String())
	}

	// POST /api/jobs/<id>/dismiss while running -> 409
	req = httptest.NewRequest(http.MethodPost, "/api/jobs/"+id+"/dismiss", nil)
	rec = httptest.NewRecorder()
	s.handleJobByID(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("dismiss running status=%d, want 409", rec.Code)
	}

	// POST /api/jobs/<id>/cancel -> 204
	req = httptest.NewRequest(http.MethodPost, "/api/jobs/"+id+"/cancel", nil)
	rec = httptest.NewRecorder()
	s.handleJobByID(rec, req)
	if rec.Code != http.StatusNoContent || !s.jobs.isStopping(id) {
		t.Fatalf("cancel status=%d stopping=%v", rec.Code, s.jobs.isStopping(id))
	}

	// Finish then dismiss -> 204
	s.jobs.finish(id, "done", "Updated 1 app")
	req = httptest.NewRequest(http.MethodPost, "/api/jobs/"+id+"/dismiss", nil)
	rec = httptest.NewRecorder()
	s.handleJobByID(rec, req)
	if rec.Code != http.StatusNoContent || len(s.jobs.list()) != 0 {
		t.Fatalf("dismiss finished status=%d remaining=%d", rec.Code, len(s.jobs.list()))
	}
}
