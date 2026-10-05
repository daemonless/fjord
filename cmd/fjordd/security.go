package main

import (
	"fmt"
	"net"
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

// DNS rebinding: a page on evil.example points that name at this host, and
// the browser then treats fjord as evil.example -- same origin, so Origin
// and Referer match Host and sameOrigin passes, and reads (.env secrets)
// were never checked at all. Only the Host header gives it away: it still
// says evil.example. So every request must name this host in a way an
// outsider cannot: an IP, localhost, the machine's own name (bare, FQDN,
// .local or under the resolver's search domains), or a name the operator
// listed for a reverse proxy.

// hostAllow is the set of names fjord answers to.
type hostAllow map[string]bool

// newHostAllow builds the set from the machine's name, the resolver's search
// domains (resolv.conf: "search" and "domain" lines) and extra, a comma list.
func newHostAllow(hostname, resolvConf, extra string) hostAllow {
	a := hostAllow{"localhost": true}
	add := func(n string) {
		if n = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(n)), "."); n != "" {
			a[n] = true
		}
	}
	add(hostname)
	short, _, _ := strings.Cut(strings.ToLower(hostname), ".")
	if short != "" {
		add(short)
		add(short + ".local")
		for _, line := range strings.Split(resolvConf, "\n") {
			f := strings.Fields(line)
			if len(f) > 1 && (f[0] == "search" || f[0] == "domain") {
				for _, d := range f[1:] {
					add(short + "." + d)
				}
			}
		}
	}
	for _, n := range strings.Split(extra, ",") {
		add(n)
	}
	return a
}

// allows reports whether a Host header (port optional) names this host.
func (a hostAllow) allows(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil || strings.HasSuffix(host, ".localhost") {
		return true
	}
	return a[host]
}

// guardHost refuses a request whose Host is not one of ours, saying how to
// allow it. /healthz stays open: it tells nothing, and a reverse proxy's
// health check may use any name.
func guardHost(allow hostAllow, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && !allow.allows(r.Host) {
			name := r.Host
			if h, _, err := net.SplitHostPort(name); err == nil {
				name = h
			}
			http.Error(w, fmt.Sprintf("fjord does not answer to %q. If that is your name for this host, "+
				"start fjordd with --allowed-hosts %s (rc.conf: fjordd_flags=\"--allowed-hosts %s\").", name, name, name),
				http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
