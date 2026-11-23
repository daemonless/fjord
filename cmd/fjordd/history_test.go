package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/daemonless/fjord/pkg/history"
)

func TestNoteForAction(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/stacks/a/update", nil)
	if n := noteForAction(r, "update"); n.action != history.Update || n.trigger != history.ByYou {
		t.Fatalf("%+v", n)
	}
	rb := r.WithContext(context.WithValue(r.Context(), isRollbackKey{}, true))
	if n := noteForAction(rb, "update"); n.action != history.Rollback {
		t.Fatalf("a rollback is recorded as one: %+v", n)
	}
	cli := r.WithContext(context.WithValue(r.Context(), localSocketKey{}, true))
	if n := noteForAction(cli, "down"); n.action != history.Stop || n.trigger != history.ByCLI {
		t.Fatalf("%+v", n)
	}
}

func TestHistoryRoutes(t *testing.T) {
	s := &server{hist: history.New(t.TempDir())}
	s.hist.Append(history.Entry{Stack: "a", Action: history.Install, Trigger: history.ByYou, Result: history.OK})
	s.hist.Append(history.Entry{Stack: "gone", Action: history.Delete, Trigger: history.ByYou, Result: history.OK})

	get := func(h func(http.ResponseWriter, *http.Request), url string) []history.Entry {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, url, nil))
		var es []history.Entry
		if err := json.Unmarshal(rec.Body.Bytes(), &es); err != nil {
			t.Fatalf("%s: %v: %s", url, err, rec.Body.String())
		}
		return es
	}
	if es := get(s.handleHistory, "/api/history"); len(es) != 2 {
		t.Fatalf("all, a deleted stack included: %+v", es)
	}
	if es := get(func(w http.ResponseWriter, r *http.Request) { s.stackHistory(w, r, "a") }, "/api/stacks/a/history?limit=5"); len(es) != 1 || es[0].Stack != "a" {
		t.Fatalf("one stack: %+v", es)
	}
	// Never null: the page can always range over it.
	empty := &server{}
	rec := httptest.NewRecorder()
	empty.handleHistory(rec, httptest.NewRequest(http.MethodGet, "/api/history", nil))
	if rec.Body.String() != "[]\n" {
		t.Fatalf("%q", rec.Body.String())
	}
}
