package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() { bcryptCost = 4 } // the minimum: tests hash a lot

// testGate is a gate in front of a mux holding the auth routes plus a
// stand-in for every other API call.
func testGate(t *testing.T, root, listen, mode, trusted string) (*authGate, http.Handler) {
	t.Helper()
	g, err := newAuthGate(root, listen, mode, trusted)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	g.routes(mux)
	ok := func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }
	mux.HandleFunc("/api/stacks", ok)
	mux.HandleFunc("/api/events", ok)
	mux.HandleFunc("/catalog/", ok)
	mux.HandleFunc("/healthz", ok)
	mux.HandleFunc("/", ok)
	return g, g.wrap(mux)
}

func do(h http.Handler, method, path, body, cookie, from string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	if from == "" {
		from = "192.168.86.40"
	}
	r.RemoteAddr = from + ":5555"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func cookieOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Errorf("cookie %+v: want HttpOnly and SameSite=Strict", c)
			}
			return c.Value
		}
	}
	t.Fatalf("no session cookie (status %d: %s)", w.Code, w.Body)
	return ""
}

func setupAccount(t *testing.T, g *authGate, h http.Handler) string {
	t.Helper()
	code, err := g.store.ensureSetupCode()
	if err != nil {
		t.Fatal(err)
	}
	w := do(h, "POST", "/api/auth/setup", `{"code":"`+code+`","user":"admin","password":"correct horse battery"}`, "", "")
	if w.Code != 200 {
		t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	return cookieOf(t, w)
}

func TestAuthLocksTheAPI(t *testing.T) {
	g, h := testGate(t, t.TempDir(), ":3567", "", "")
	for _, p := range []string{"/api/stacks", "/api/events", "/catalog/x/icon.svg"} {
		if w := do(h, "GET", p, "", "", ""); w.Code != 401 {
			t.Errorf("%s without a session: %d, want 401", p, w.Code)
		}
	}
	for _, p := range []string{"/healthz", "/", "/assets/index.js", "/api/auth/state"} {
		if w := do(h, "GET", p, "", "", ""); w.Code != 200 {
			t.Errorf("%s: %d, want 200 (open)", p, w.Code)
		}
	}
	if w := do(h, "GET", "/api/stacks", "", "made-up", ""); w.Code != 401 {
		t.Errorf("a made-up cookie: %d, want 401", w.Code)
	}
	c := setupAccount(t, g, h)
	if w := do(h, "GET", "/api/stacks", "", c, ""); w.Code != 200 {
		t.Errorf("signed in: %d, want 200", w.Code)
	}
}

func TestSetupCodeWorksOnce(t *testing.T) {
	root := t.TempDir()
	g, h := testGate(t, root, ":3567", "", "")
	g.logAuthStart()
	b, err := os.ReadFile(filepath.Join(root, "setup-code"))
	if err != nil {
		t.Fatalf("no setup code written at start: %v", err)
	}
	code := strings.TrimSpace(string(b))
	if st, _ := os.Stat(filepath.Join(root, "setup-code")); st.Mode().Perm() != 0o600 {
		t.Errorf("setup-code mode %v, want 0600", st.Mode().Perm())
	}
	var state map[string]any
	json.Unmarshal(do(h, "GET", "/api/auth/state", "", "", "").Body.Bytes(), &state)
	if state["account"] != false || state["setupCodePath"] != filepath.Join(root, "setup-code") {
		t.Errorf("state before setup: %v", state)
	}
	if w := do(h, "POST", "/api/auth/setup", `{"code":"AAAA-BBBB-CCCC","user":"admin","password":"correct horse battery"}`, "", ""); w.Code != 403 {
		t.Errorf("wrong code: %d, want 403", w.Code)
	}
	if w := do(h, "POST", "/api/auth/setup", `{"code":"`+code+`","user":"admin","password":"short"}`, "", ""); w.Code != 400 {
		t.Errorf("short password: %d, want 400", w.Code)
	}
	// Typed in lower case without dashes still counts.
	loose := strings.ToLower(strings.ReplaceAll(code, "-", ""))
	w := do(h, "POST", "/api/auth/setup", `{"code":"`+loose+`","user":"admin","password":"correct horse battery"}`, "", "")
	if w.Code != 200 {
		t.Fatalf("right code: %d %s", w.Code, w.Body)
	}
	cookieOf(t, w)
	if _, err := os.Stat(filepath.Join(root, "setup-code")); !os.IsNotExist(err) {
		t.Errorf("setup code still there after use: %v", err)
	}
	if w := do(h, "POST", "/api/auth/setup", `{"code":"`+code+`","user":"evil","password":"correct horse battery"}`, "", ""); w.Code != 409 {
		t.Errorf("second setup: %d, want 409", w.Code)
	}
	if st, _ := os.Stat(filepath.Join(root, "auth.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("auth.json mode %v, want 0600", st.Mode().Perm())
	}
	raw, _ := os.ReadFile(filepath.Join(root, "auth.json"))
	if bytes.Contains(raw, []byte("correct horse")) {
		t.Error("auth.json holds the password")
	}
}

func TestPasswdBeforeFirstStart(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := runPasswd([]string{"--stacks-dir", filepath.Join(root, "stacks"), "--password-stdin"}, strings.NewReader("correct horse battery\n"), &out); err != nil {
		t.Fatal(err)
	}
	g, h := testGate(t, root, ":3567", "", "")
	g.logAuthStart()
	if _, err := os.Stat(filepath.Join(root, "setup-code")); !os.IsNotExist(err) {
		t.Errorf("a setup code was made though the account exists: %v", err)
	}
	w := do(h, "POST", "/api/auth/login", `{"user":"admin","password":"correct horse battery"}`, "", "")
	if w.Code != 200 {
		t.Fatalf("sign in as admin: %d %s", w.Code, w.Body)
	}
	if err := runPasswd([]string{"--stacks-dir", filepath.Join(root, "stacks"), "--password-stdin"}, strings.NewReader("short\n"), &out); err == nil {
		t.Error("a short password was accepted")
	}
}

func TestPasswdEndsSessions(t *testing.T) {
	root := t.TempDir()
	g, h := testGate(t, root, ":3567", "", "")
	c := setupAccount(t, g, h)
	var out bytes.Buffer
	// The daemon's copy was read already; passwd writes the file under it.
	time.Sleep(10 * time.Millisecond) // a new mtime
	if err := runPasswd([]string{"--stacks-dir", filepath.Join(root, "stacks"), "--user", "me", "--password-stdin"}, strings.NewReader("another long password\n"), &out); err != nil {
		t.Fatal(err)
	}
	if w := do(h, "GET", "/api/stacks", "", c, ""); w.Code != 401 {
		t.Errorf("session after passwd: %d, want 401", w.Code)
	}
	if w := do(h, "POST", "/api/auth/login", `{"user":"me","password":"another long password"}`, "", ""); w.Code != 200 {
		t.Errorf("sign in with the new password: %d %s", w.Code, w.Body)
	}
}

func TestSessionSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	g, h := testGate(t, root, ":3567", "", "")
	c := setupAccount(t, g, h)
	_, h2 := testGate(t, root, ":3567", "", "") // a new daemon on the same root
	if w := do(h2, "GET", "/api/stacks", "", c, ""); w.Code != 200 {
		t.Errorf("after a restart: %d, want 200", w.Code)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "auth.json"))
	if bytes.Contains(raw, []byte(c)) {
		t.Error("auth.json holds a live cookie")
	}
}

func TestSessionExpires(t *testing.T) {
	g, h := testGate(t, t.TempDir(), ":3567", "", "")
	c := setupAccount(t, g, h)
	start := time.Now()
	g.store.now = func() time.Time { return start.Add(29 * 24 * time.Hour) }
	if w := do(h, "GET", "/api/stacks", "", c, ""); w.Code != 200 {
		t.Fatalf("day 29: %d", w.Code)
	}
	// Used on day 29: renewed, so day 58 is still inside 30 days of use.
	g.store.now = func() time.Time { return start.Add(58 * 24 * time.Hour) }
	if w := do(h, "GET", "/api/stacks", "", c, ""); w.Code != 200 {
		t.Errorf("day 58 after use on day 29: %d, want 200", w.Code)
	}
	g.store.now = func() time.Time { return start.Add(89 * 24 * time.Hour) }
	if w := do(h, "GET", "/api/stacks", "", c, ""); w.Code != 401 {
		t.Errorf("31 days unused: %d, want 401", w.Code)
	}
}

func TestWrongPasswordBackoff(t *testing.T) {
	g, h := testGate(t, t.TempDir(), ":3567", "", "")
	setupAccount(t, g, h)
	now := time.Now()
	g.limit.now = func() time.Time { return now }
	for i := 1; i <= signinFreeTries; i++ {
		if w := do(h, "POST", "/api/auth/login", `{"user":"admin","password":"wrong wrong wrong"}`, "", "10.0.0.9"); w.Code != 401 {
			t.Fatalf("try %d: %d, want 401", i, w.Code)
		}
	}
	w := do(h, "POST", "/api/auth/login", `{"user":"admin","password":"correct horse battery"}`, "", "10.0.0.9")
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Errorf("right password while backing off: %d, want 429 with Retry-After", w.Code)
	}
	if w := do(h, "POST", "/api/auth/login", `{"user":"admin","password":"correct horse battery"}`, "", "10.0.0.10"); w.Code != 200 {
		t.Errorf("another address: %d, want 200", w.Code)
	}
	now = now.Add(2 * time.Second)
	if w := do(h, "POST", "/api/auth/login", `{"user":"admin","password":"correct horse battery"}`, "", "10.0.0.9"); w.Code != 200 {
		t.Errorf("after the wait: %d, want 200", w.Code)
	}
	if w := do(h, "POST", "/api/auth/login", `{"user":"root","password":"correct horse battery"}`, "", "10.0.0.11"); w.Code != 401 {
		t.Errorf("wrong user: %d, want 401", w.Code)
	}
	f, _ := g.store.read()
	var failed bool
	for _, e := range f.Activity {
		failed = failed || (e.Kind == "failed" && e.Addr == "10.0.0.9")
	}
	if !failed {
		t.Errorf("no failed sign-ins in the activity: %+v", f.Activity)
	}
}

func TestPasswordChangeAndSessions(t *testing.T) {
	g, h := testGate(t, t.TempDir(), ":3567", "", "")
	laptop := setupAccount(t, g, h)
	phone := cookieOf(t, do(h, "POST", "/api/auth/login", `{"user":"admin","password":"correct horse battery"}`, "", "192.168.86.77"))

	var acct struct {
		Sessions []sessionInfo
		Activity []authEvent
	}
	json.Unmarshal(do(h, "GET", "/api/auth/account", "", laptop, "").Body.Bytes(), &acct)
	if len(acct.Sessions) != 2 {
		t.Fatalf("sessions: %+v", acct.Sessions)
	}
	var newAddr bool
	for _, e := range acct.Activity {
		newAddr = newAddr || (e.Kind == "new-address" && e.Addr == "192.168.86.77")
	}
	if !newAddr {
		t.Errorf("no new-address sign-in in the activity: %+v", acct.Activity)
	}
	// Sign the phone out from the laptop.
	var phoneID string
	for _, s := range acct.Sessions {
		if !s.Current {
			phoneID = s.ID
		}
	}
	if w := do(h, "DELETE", "/api/auth/sessions/"+phoneID, "", laptop, ""); w.Code != 204 {
		t.Fatalf("end phone session: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/stacks", "", phone, ""); w.Code != 401 {
		t.Errorf("phone after sign-out: %d, want 401", w.Code)
	}

	phone = cookieOf(t, do(h, "POST", "/api/auth/login", `{"user":"admin","password":"correct horse battery"}`, "", "192.168.86.77"))
	if w := do(h, "POST", "/api/auth/password", `{"current":"wrong wrong wrong","password":"brand new password"}`, laptop, ""); w.Code != 403 {
		t.Errorf("change without the current password: %d, want 403", w.Code)
	}
	if w := do(h, "POST", "/api/auth/password", `{"current":"correct horse battery","password":"brand new password"}`, laptop, ""); w.Code != 204 {
		t.Fatalf("change: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/stacks", "", laptop, ""); w.Code != 200 {
		t.Errorf("the browser that changed it: %d, want 200", w.Code)
	}
	if w := do(h, "GET", "/api/stacks", "", phone, ""); w.Code != 401 {
		t.Errorf("other browser after a password change: %d, want 401", w.Code)
	}

	phone = cookieOf(t, do(h, "POST", "/api/auth/login", `{"user":"admin","password":"brand new password"}`, "", "192.168.86.77"))
	if w := do(h, "POST", "/api/auth/sessions", `{"current":"brand new password"}`, laptop, ""); w.Code != 204 {
		t.Fatalf("sign out everywhere: %d %s", w.Code, w.Body)
	}
	if do(h, "GET", "/api/stacks", "", phone, "").Code != 401 || do(h, "GET", "/api/stacks", "", laptop, "").Code != 200 {
		t.Error("sign out everywhere should end the phone and keep the laptop")
	}
	if w := do(h, "POST", "/api/auth/logout", "", laptop, ""); w.Code != 204 {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := do(h, "GET", "/api/stacks", "", laptop, ""); w.Code != 401 {
		t.Errorf("after logout: %d, want 401", w.Code)
	}
}

func TestAuthOff(t *testing.T) {
	if _, err := newAuthGate(t.TempDir(), ":3567", "none", ""); err == nil {
		t.Error("FJORD_AUTH=none on every address with no proxy was accepted")
	}
	if _, err := newAuthGate(t.TempDir(), ":3567", "nope", ""); err == nil {
		t.Error("an unknown FJORD_AUTH was accepted")
	}
	if _, err := newAuthGate(t.TempDir(), ":3567", "none", "proxy.lan"); err == nil {
		t.Error("a name as trusted proxy was accepted")
	}
	_, h := testGate(t, t.TempDir(), "127.0.0.1:3567", "none", "")
	if w := do(h, "GET", "/api/stacks", "", "", "127.0.0.1"); w.Code != 200 {
		t.Errorf("loopback with auth off: %d, want 200", w.Code)
	}
	_, h = testGate(t, t.TempDir(), ":3567", "none", "10.88.0.0/16")
	if w := do(h, "GET", "/api/stacks", "", "", "10.88.0.5"); w.Code != 200 {
		t.Errorf("the proxy with auth off: %d, want 200", w.Code)
	}
	for _, p := range []string{"/api/stacks", "/"} {
		if w := do(h, "GET", p, "", "", "192.168.86.40"); w.Code != 403 {
			t.Errorf("%s from outside the proxy with auth off: %d, want 403", p, w.Code)
		}
	}
	if w := do(h, "GET", "/healthz", "", "", "192.168.86.40"); w.Code != 200 {
		t.Errorf("/healthz with auth off: %d, want 200", w.Code)
	}
}

func TestClientAddrBehindProxy(t *testing.T) {
	g, _ := testGate(t, t.TempDir(), ":3567", "", "10.88.0.5")
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 192.168.86.40")
	r.RemoteAddr = "10.88.0.5:4444"
	if got := g.clientAddr(r); got != "192.168.86.40" {
		t.Errorf("via the proxy: %s, want the last hop 192.168.86.40", got)
	}
	r.RemoteAddr = "192.168.86.99:4444"
	if got := g.clientAddr(r); got != "192.168.86.99" {
		t.Errorf("not via the proxy: %s, want the peer (X-Forwarded-For ignored)", got)
	}
}
