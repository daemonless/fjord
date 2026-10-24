package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "fjord_session"

// authGate is the sign-in boundary in front of every route. With auth on,
// /api/ and /catalog/ need a session (except the sign-in calls themselves);
// the UI's files and /healthz stay open. With FJORD_AUTH=none, only loopback
// and the trusted proxies get in at all.
type authGate struct {
	store   *authStore
	off     bool
	proxies []netip.Prefix
	limit   *signinLimit
}

// openPaths need no session: the page asks them before anyone signs in.
var openPaths = map[string]bool{
	"/api/auth/state": true,
	"/api/auth/login": true,
	"/api/auth/setup": true,
}

// newAuthGate reads FJORD_AUTH and FJORD_TRUSTED_PROXY. Turning auth off is
// refused unless only loopback or a named proxy can reach fjordd.
func newAuthGate(root, listen, mode, trusted string) (*authGate, error) {
	g := &authGate{store: newAuthStore(root), limit: newSigninLimit()}
	for _, s := range strings.Split(trusted, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, aerr := netip.ParseAddr(s)
			if aerr != nil {
				return nil, fmt.Errorf("FJORD_TRUSTED_PROXY: %q is not an address or a range (10.88.0.5, 10.88.0.0/16)", s)
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		g.proxies = append(g.proxies, p.Masked())
	}
	switch mode {
	case "", "password":
	case "none":
		if !loopbackListen(listen) && len(g.proxies) == 0 {
			return nil, fmt.Errorf("FJORD_AUTH=none: anyone who can reach %s would be root on this host. "+
				"Listen on loopback only (FJORD_LISTEN=127.0.0.1:3567), or name the proxy in front with "+
				"FJORD_TRUSTED_PROXY (an address or a range); everything else is then refused", listen)
		}
		g.off = true
	default:
		return nil, fmt.Errorf("FJORD_AUTH=%q: use password (the default) or none", mode)
	}
	return g, nil
}

func loopbackListen(listen string) bool {
	h, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	a, err := netip.ParseAddr(strings.Trim(h, "[]"))
	return err == nil && a.IsLoopback() || h == "localhost"
}

func (g *authGate) trusted(a netip.Addr) bool {
	for _, p := range g.proxies {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// clientAddr is the address a person signs in from: the peer, or the
// X-Forwarded-For hop a trusted proxy added.
func (g *authGate) clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if g.trusted(peer) {
		hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if last := strings.TrimSpace(hops[len(hops)-1]); last != "" {
			if a, err := netip.ParseAddr(last); err == nil {
				return a.Unmap().String()
			}
		}
	}
	return peer.Unmap().String()
}

func (g *authGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if g.off {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			a, err := netip.ParseAddr(host)
			if err != nil || !(a.Unmap().IsLoopback() || g.trusted(a)) {
				http.Error(w, "fjord's sign-in is off, so only the proxy in front of it may connect", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if (strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/catalog/")) && !openPaths[p] {
			if g.sessionOf(r) == "" {
				writeAuthError(w, http.StatusUnauthorized, "sign in first")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (g *authGate) sessionOf(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return g.store.session(c.Value)
}

func writeAuthError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (g *authGate) routes(mux *http.ServeMux) {
	mux.HandleFunc("/api/auth/state", g.handleState)
	mux.HandleFunc("/api/auth/setup", g.handleSetup)
	mux.HandleFunc("/api/auth/login", g.handleLogin)
	mux.HandleFunc("/api/auth/logout", g.handleLogout)
	mux.HandleFunc("/api/auth/account", g.handleAccount)
	mux.HandleFunc("/api/auth/password", g.handlePassword)
	mux.HandleFunc("/api/auth/sessions/", g.handleSessionEnd)
	mux.HandleFunc("/api/auth/sessions", g.handleSessionsEndAll)
}

// handleState: what the page shows first -- Set up, Sign in, or the app.
func (g *authGate) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f, _ := g.store.read()
	st := map[string]any{
		"off":      g.off,
		"account":  f.Hash != "",
		"signedIn": g.off || g.sessionOf(r) != "",
	}
	if f.Hash == "" && !g.off {
		st["setupCodePath"] = g.store.setupCodePath()
	}
	if st["signedIn"] == true && !g.off {
		st["user"] = f.User
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(st)
}

type credentials struct {
	Code     string `json:"code,omitempty"`
	User     string `json:"user"`
	Password string `json:"password"`
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(v); err != nil {
		writeAuthError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return false
	}
	return true
}

// wait answers 429 while an address is backing off after wrong guesses.
func (g *authGate) wait(w http.ResponseWriter, addr string) bool {
	if d := g.limit.wait(addr); d > 0 {
		secs := int(d.Round(time.Second) / time.Second)
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeAuthError(w, http.StatusTooManyRequests, fmt.Sprintf("too many wrong tries: wait %d seconds", secs))
		return true
	}
	return false
}

func (g *authGate) failed(addr, what string) {
	n := g.limit.fail(addr)
	log.Printf("auth: wrong %s from %s (%d in a row)", what, addr, n)
	if n%signinFreeTries == 0 {
		g.store.note(authEvent{Time: g.store.now(), Kind: "failed", Addr: addr,
			Text: fmt.Sprintf("%d failed sign-ins (wrong %s)", n, what)})
	}
}

func (g *authGate) handleSetup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decodeJSON(w, r, &c) {
		return
	}
	addr := g.clientAddr(r)
	if g.store.hasAccount() {
		writeAuthError(w, http.StatusConflict, errAccountExists.Error())
		return
	}
	if g.wait(w, addr) {
		return
	}
	if !g.store.checkSetupCode(c.Code) {
		g.failed(addr, "setup code")
		writeAuthError(w, http.StatusForbidden, "that is not the setup code: on the host, cat "+g.store.setupCodePath())
		return
	}
	if err := checkNewPassword(strings.TrimSpace(c.User), c.Password); err != nil {
		writeAuthError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := g.store.setAccount(c.User, c.Password, "", addr, true); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errAccountExists) {
			code = http.StatusConflict
		}
		writeAuthError(w, code, err.Error())
		return
	}
	g.limit.ok(addr)
	g.store.removeSetupCode()
	log.Printf("auth: account %s created from %s", strings.TrimSpace(c.User), addr)
	g.signIn(w, r, addr)
}

func (g *authGate) handleLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decodeJSON(w, r, &c) {
		return
	}
	addr := g.clientAddr(r)
	if g.wait(w, addr) {
		return
	}
	if !g.store.checkPassword(c.User, c.Password) {
		g.failed(addr, "user name or password")
		writeAuthError(w, http.StatusUnauthorized, "wrong user name or password")
		return
	}
	g.limit.ok(addr)
	log.Printf("auth: %s signed in from %s", strings.TrimSpace(c.User), addr)
	g.signIn(w, r, addr)
}

func (g *authGate) signIn(w http.ResponseWriter, r *http.Request, addr string) {
	token, err := g.store.newSession(r.UserAgent(), addr)
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		MaxAge: int(sessionTTL / time.Second), HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: g.overTLS(r),
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (g *authGate) overTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	a, err := netip.ParseAddr(host)
	return err == nil && g.trusted(a) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (g *authGate) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if id := g.sessionOf(r); id != "" {
		g.store.endSessions([]string{id}, "")
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

type sessionInfo struct {
	ID       string    `json:"id"`
	Browser  string    `json:"browser"`
	Addr     string    `json:"addr"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed"`
	Current  bool      `json:"current"`
}

// handleAccount: Settings > Account -- the user, where you are signed in,
// and the recent sign-in activity (newest first).
func (g *authGate) handleAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f, err := g.store.read()
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cur := g.sessionOf(r)
	now := g.store.now()
	sessions := []sessionInfo{}
	for id, s := range f.Sessions {
		if now.Sub(s.LastUsed) > sessionTTL {
			continue
		}
		// The id is the cookie's hash: it names a session without being one.
		sessions = append(sessions, sessionInfo{ID: id[:16], Browser: s.Browser, Addr: s.Addr, Created: s.Created, LastUsed: s.LastUsed, Current: id == cur})
	}
	slices.SortFunc(sessions, func(a, b sessionInfo) int { return b.LastUsed.Compare(a.LastUsed) })
	activity := make([]authEvent, 0, len(f.Activity))
	for i := len(f.Activity) - 1; i >= 0; i-- {
		activity = append(activity, f.Activity[i])
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"user": f.User, "sessions": sessions, "activity": activity})
}

// handlePassword changes the password (and the user name). It asks for the
// current password and ends every other session.
func (g *authGate) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current  string `json:"current"`
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	addr := g.clientAddr(r)
	if !g.recheck(w, addr, req.Current) {
		return
	}
	f, _ := g.store.read()
	user := strings.TrimSpace(req.User)
	if user == "" {
		user = f.User
	}
	if err := g.store.setAccount(user, req.Password, g.sessionOf(r), addr, false); err != nil {
		writeAuthError(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Printf("auth: password changed from %s; other sessions ended", addr)
	w.WriteHeader(http.StatusNoContent)
}

// recheck asks for the password again before a change that locks people out.
func (g *authGate) recheck(w http.ResponseWriter, addr, password string) bool {
	if g.wait(w, addr) {
		return false
	}
	f, _ := g.store.read()
	if !g.store.checkPassword(f.User, password) {
		g.failed(addr, "current password")
		writeAuthError(w, http.StatusForbidden, "that is not the current password")
		return false
	}
	g.limit.ok(addr)
	return true
}

// handleSessionEnd: DELETE /api/auth/sessions/<id> signs one browser out.
func (g *authGate) handleSessionEnd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	short := strings.TrimPrefix(r.URL.Path, "/api/auth/sessions/")
	f, _ := g.store.read()
	var ids []string
	for id := range f.Sessions {
		if len(short) == 16 && strings.HasPrefix(id, short) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		writeAuthError(w, http.StatusNotFound, "no such session")
		return
	}
	if err := g.store.endSessions(ids, ""); err != nil {
		writeAuthError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSessionsEndAll: POST /api/auth/sessions {"current": "..."} signs out
// every other browser.
func (g *authGate) handleSessionsEndAll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	addr := g.clientAddr(r)
	if !g.recheck(w, addr, req.Current) {
		return
	}
	if err := g.store.endSessions(nil, g.sessionOf(r)); err != nil {
		writeAuthError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("auth: every other session ended from %s", addr)
	w.WriteHeader(http.StatusNoContent)
}

// logAuthStart says, at start, how to get in.
func (g *authGate) logAuthStart() {
	if g.off {
		log.Printf("auth: OFF (FJORD_AUTH=none): only loopback and %v may connect, with no sign-in", g.proxies)
		return
	}
	if g.store.hasAccount() {
		return
	}
	if _, err := g.store.ensureSetupCode(); err != nil {
		log.Printf("auth: no account yet, and the setup code could not be written: %v -- run `%s` on this host", err, passwdCommand())
		return
	}
	log.Printf("auth: no account yet. Open the web UI and enter the setup code: on this host, cat %s (or run `%s`)", g.store.setupCodePath(), passwdCommand())
}

// Wrong guesses: after signinFreeTries from an address, each further one
// doubles the wait, up to signinMaxWait. In memory: a restart forgets.

const (
	signinFreeTries = 5
	signinMaxWait   = 5 * time.Minute
)

type signinLimit struct {
	mu  sync.Mutex
	m   map[string]*signinTries
	now func() time.Time
}

type signinTries struct {
	n     int
	until time.Time
	last  time.Time
}

func newSigninLimit() *signinLimit {
	return &signinLimit{m: map[string]*signinTries{}, now: time.Now}
}

func (l *signinLimit) wait(addr string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t := l.m[addr]; t != nil {
		return t.until.Sub(l.now())
	}
	return 0
}

func (l *signinLimit) fail(addr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for a, t := range l.m {
		if now.Sub(t.last) > 24*time.Hour {
			delete(l.m, a)
		}
	}
	t := l.m[addr]
	if t == nil {
		t = &signinTries{}
		l.m[addr] = t
	}
	t.n++
	t.last = now
	if t.n >= signinFreeTries {
		d := time.Second << min(t.n-signinFreeTries, 10)
		t.until = now.Add(min(d, signinMaxWait))
	}
	return t.n
}

func (l *signinLimit) ok(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, addr)
}

// passwdCommand is how to run fjordd passwd for this daemon: an interactive
// root shell lacks the daemon's environment (rc.conf fjordd_env, a systemd
// Environment=), so a stacks dir other than the default is named.
func passwdCommand() string {
	if d := os.Getenv("FJORD_STACKS_DIR"); d != "" && d != defaultStacksDir {
		return "fjordd passwd --stacks-dir " + d
	}
	return "fjordd passwd"
}

// authEnv is FJORD_AUTH's value; a helper so tests and main read one place.
func authEnv() string { return strings.ToLower(strings.TrimSpace(os.Getenv("FJORD_AUTH"))) }
