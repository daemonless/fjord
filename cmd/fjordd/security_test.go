package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGuardMutations(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := guardMutations(ok)
	cases := []struct {
		name    string
		method  string
		headers map[string]string
		body    string
		want    int
	}{
		{"get anywhere", "GET", map[string]string{"Origin": "http://evil.example"}, "", 200},
		{"curl no headers", "POST", nil, "", 200},
		{"same-origin json", "POST", map[string]string{"Origin": "http://saturn:3567", "Content-Type": "application/json"}, "{}", 200},
		{"same-origin referer only", "DELETE", map[string]string{"Referer": "http://saturn:3567/#/stacks/1"}, "", 200},
		{"cross-origin", "POST", map[string]string{"Origin": "http://evil.example", "Content-Type": "application/json"}, "{}", 403},
		{"cross-site fetch metadata", "POST", map[string]string{"Sec-Fetch-Site": "cross-site", "Content-Type": "application/json"}, "{}", 403},
		{"form post", "POST", map[string]string{"Origin": "http://saturn:3567", "Content-Type": "application/x-www-form-urlencoded"}, "a=b", 415},
		// "null" is an opaque origin: a sandboxed iframe, a file:// page or a
		// data: URL. It is a browser saying "somewhere you should not trust",
		// not a client saying nothing -- so it is refused, not waved through.
		// This case asserted 200 until the audit found it.
		{"null origin", "POST", map[string]string{"Origin": "null"}, "", 403},
		{"null origin, no referer, ws upgrade shape", "POST", map[string]string{"Origin": "null", "Content-Type": "application/json"}, "{}", 403},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://saturn:3567/api/x", strings.NewReader(c.body))
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, rr.Code, c.want)
		}
	}
}

func TestHasControlChars(t *testing.T) {
	if hasControlChars("plain value") || hasControlChars("tab\tok") {
		t.Error("false positive")
	}
	if !hasControlChars("a\nB=evil") || !hasControlChars("x\r") || !hasControlChars("\x00") {
		t.Error("missed control char")
	}
}

// sameOrigin is what CheckOrigin hands the WebSocket upgrader, so the exec
// shell is only as safe as this function -- test it directly rather than
// through guardMutations, which never sees a GET.
func TestSameOriginForWebSocketUpgrade(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"no headers (curl)", nil, true},
		{"same origin", map[string]string{"Origin": "http://saturn:3567"}, true},
		{"other host", map[string]string{"Origin": "http://evil.example"}, false},
		// The one the audit found: a sandboxed iframe sends this and no
		// Referer, which used to reach the "must be curl" default.
		{"opaque origin", map[string]string{"Origin": "null"}, false},
		{"opaque origin with referer", map[string]string{"Origin": "null", "Referer": "http://saturn:3567/"}, false},
		{"cross-site metadata", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "http://saturn:3567/api/stacks/x/exec?container=x", nil)
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		if got := sameOrigin(req); got != c.want {
			t.Errorf("%s: sameOrigin = %v, want %v", c.name, got, c.want)
		}
	}
}
