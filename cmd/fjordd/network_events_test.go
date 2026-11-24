package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A network change that succeeds tells open pages; a read or a refused
// change does not.
func TestNetworksChangedPublishes(t *testing.T) {
	s := &server{events: newEventHub()}
	ch := s.events.subscribe()
	defer s.events.unsubscribe(ch)
	ok := func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{}")) }
	refused := func(w http.ResponseWriter, r *http.Request) { http.Error(w, "in use", http.StatusConflict) }
	cases := []struct {
		name   string
		method string
		h      http.HandlerFunc
		want   bool
	}{
		{"create", http.MethodPost, ok, true},
		{"edit", http.MethodPut, ok, true},
		{"delete", http.MethodDelete, ok, true},
		{"list", http.MethodGet, ok, false},
		{"refused delete", http.MethodDelete, refused, false},
	}
	for _, c := range cases {
		s.networksChanged(c.h)(httptest.NewRecorder(), httptest.NewRequest(c.method, "/api/networks/x", nil))
		var got bool
		select {
		case b := <-ch:
			got = string(b) == `{"type":"networks"}`
		default:
		}
		if got != c.want {
			t.Errorf("%s: published %v, want %v", c.name, got, c.want)
		}
	}
}
