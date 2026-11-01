package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The shell is never stored (a restored tab must fetch it), the hashed
// assets are kept for a year.
func TestCacheControlShellNeverStored(t *testing.T) {
	h := cacheControl(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for path, want := range map[string]string{"/": "no-store", "/index.html": "no-store", "/theme.js": "no-store", "/assets/index-abc.js": "public, max-age=31536000, immutable"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if got := rec.Header().Get("Cache-Control"); got != want {
			t.Fatalf("%s: Cache-Control %q, want %q", path, got, want)
		}
	}
}
