package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/stack"
)

// The offer goes to stacks no one has answered for: one turned off on
// purpose is an answer. Answered once, it is not made again.
func TestAutoUpdateOffer(t *testing.T) {
	root := t.TempDir()
	s := &server{manager: stack.NewManager(root + "/stacks"), fjordRoot: root}
	for name, policy := range map[string]string{"old": "", "chose-off": "off", "new": "minor"} {
		if err := s.manager.Save(&stack.Stack{Name: name, Compose: "services:\n  web:\n    image: x\n"}); err != nil {
			t.Fatal(err)
		}
		p := policy
		s.manager.UpdateState(name, func(*stack.State) *stack.State {
			return &stack.State{SchemaVersion: 1, DesiredState: "running", UpdatePolicy: p}
		})
	}
	if got := strings.Join(s.unasked(), ","); got != "old" {
		t.Fatalf("offered to %q", got)
	}
	rec := httptest.NewRecorder()
	s.autoUpdateOffer(rec, httptest.NewRequest(http.MethodPost, "/api/autoupdate/offer", strings.NewReader(`{"answer":"all"}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"old"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for name, want := range map[string]string{"old": "minor", "chose-off": "off", "new": "minor"} {
		if st, _ := s.manager.LoadState(name); st.UpdatePolicy != want {
			t.Errorf("%s: %q, want %q", name, st.UpdatePolicy, want)
		}
	}
	if len(s.unasked()) != 0 {
		t.Fatal("offered again")
	}
}
