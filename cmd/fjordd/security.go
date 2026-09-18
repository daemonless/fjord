package main

import (
	"net/http"
	"net/url"
	"strings"
)

// Pre-auth hardening. fjordd runs as root and has no login yet, so the one
// boundary that exists must hold: a web page the operator happens to visit
// must not be able to drive the API. Browsers always send Origin on
// cross-site POSTs and WebSocket upgrades (and Sec-Fetch-Site on modern
// ones), so mutating requests are accepted only when those say same-origin.
// Non-browser clients (curl, scripts) send neither and are unaffected.

// sameOrigin reports whether a request's Origin/Referer, when present, names
// this server. Absent headers pass: that's curl, not a browser.
func sameOrigin(r *http.Request) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
		return false
	}
	check := func(raw string) bool {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	}
	// "null" is an opaque origin -- a sandboxed iframe, a file:// page, a
	// data: URL -- not an absent one. Falling through to the Referer check
	// (which such a page also omits) reached the "no headers, must be curl"
	// default and let it through.
	if o := r.Header.Get("Origin"); o != "" {
		if o == "null" {
			return false
		}
		return check(o)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		return check(ref)
	}
	return true
}

// guardMutations wraps the API mux: cross-origin mutating requests are
// refused, and a request that carries a body must declare it as JSON (a
// plain HTML form can't do that, which is the point).
func guardMutations(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if !sameOrigin(r) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
			if r.ContentLength != 0 {
				ct := strings.ToLower(r.Header.Get("Content-Type"))
				if !strings.HasPrefix(ct, "application/json") {
					http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// hasControlChars reports whether s carries newlines or other control
// characters -- anything that could break out of a KEY=VALUE line in .env.
func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\t' || r == 0x7f {
			return true
		}
	}
	return false
}
