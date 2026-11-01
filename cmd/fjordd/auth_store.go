package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// The one account and its sessions, in <root>/auth.json (0600). `fjordd
// passwd` writes the same file while the daemon runs, so every change is
// load-change-save under an flock, and reads reload when the file changed.

const (
	sessionTTL     = 30 * 24 * time.Hour
	sessionTouch   = time.Hour // last-used is written at most this often
	maxPasswordLen = 72        // bcrypt reads no further
	activityKeep   = 50
	seenAddrsKeep  = 100
)

// bcryptCost is a variable so tests can run fast.
var bcryptCost = 12

type authSession struct {
	Browser  string    `json:"browser"`
	Addr     string    `json:"addr"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed"`
}

// authEvent is one line of the Account page's sign-in activity.
type authEvent struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"` // failed, password, user, new-address, setup
	Addr string    `json:"addr,omitempty"`
	Text string    `json:"text"`
}

type authFile struct {
	User string `json:"user,omitempty"`
	Hash string `json:"hash,omitempty"`
	// Sessions are keyed by the SHA-256 of the cookie: the file holds no
	// live cookie.
	Sessions  map[string]*authSession `json:"sessions,omitempty"`
	Activity  []authEvent             `json:"activity,omitempty"`
	SeenAddrs []string                `json:"seenAddrs,omitempty"`
}

type authStore struct {
	path string
	mu   sync.Mutex
	f    authFile
	mod  time.Time // mtime of what f was read from
	now  func() time.Time
}

func newAuthStore(root string) *authStore {
	return &authStore{path: filepath.Join(root, "auth.json"), now: time.Now}
}

func (a *authStore) setupCodePath() string {
	return filepath.Join(filepath.Dir(a.path), "setup-code")
}

// refresh rereads the file when someone else (fjordd passwd) wrote it.
// Caller holds a.mu.
func (a *authStore) refresh() error {
	st, err := os.Stat(a.path)
	if errors.Is(err, os.ErrNotExist) {
		a.f, a.mod = authFile{}, time.Time{}
		return nil
	}
	if err != nil {
		return err
	}
	if st.ModTime().Equal(a.mod) {
		return nil
	}
	b, err := os.ReadFile(a.path)
	if err != nil {
		return err
	}
	var f authFile
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("%s: %w", a.path, err)
	}
	a.f, a.mod = f, st.ModTime()
	return nil
}

// change runs fn on the current file and saves it, holding the flock so
// fjordd and fjordd passwd never write over each other.
func (a *authStore) change(fn func(f *authFile) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(a.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	a.mod = time.Time{} // always reread under the lock
	if err := a.refresh(); err != nil {
		return err
	}
	if err := fn(&a.f); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a.f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.path), ".auth-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), a.path); err != nil {
		return err
	}
	if st, err := os.Stat(a.path); err == nil {
		a.mod = st.ModTime()
	}
	return nil
}

func (a *authStore) read() (authFile, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.refresh()
	return a.f, err
}

func (a *authStore) hasAccount() bool {
	f, err := a.read()
	return err == nil && f.Hash != ""
}

func checkNewPassword(user, password string) error {
	switch {
	case strings.TrimSpace(user) == "" || hasControlChars(user) || len(user) > 64:
		return errors.New("choose a user name (up to 64 characters)")
	case password == "":
		// No length rule: the page warns about a weak password and the
		// person decides, as Fedora's installer does. Sign-in is
		// rate-limited, which is what stops guessing.
		return errors.New("enter a password")
	case len(password) > maxPasswordLen:
		return fmt.Errorf("the password can have at most %d bytes", maxPasswordLen)
	}
	return nil
}

var errAccountExists = errors.New("the account already exists: sign in")

// setAccount sets the user name and password and ends every session but
// keep (the browser that made the change; "" ends all). onlyNew refuses when
// an account exists (setup: two right codes at once make one account).
func (a *authStore) setAccount(user, password, keep, addr string, onlyNew bool) error {
	user = strings.TrimSpace(user)
	if err := checkNewPassword(user, password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return err
	}
	return a.change(func(f *authFile) error {
		if onlyNew && f.Hash != "" {
			return errAccountExists
		}
		kind, text := "password", "Password changed"
		switch {
		case f.Hash == "":
			kind, text = "setup", "Account created"
		case f.User != user:
			kind, text = "user", fmt.Sprintf("User name changed to %s, password set", user)
		}
		f.User, f.Hash = user, string(hash)
		for id := range f.Sessions {
			if id != keep {
				delete(f.Sessions, id)
			}
		}
		f.addEvent(authEvent{Time: a.now(), Kind: kind, Addr: addr, Text: text})
		return nil
	})
}

// dummyHash keeps a wrong user name, or no account, as slow as a wrong
// password.
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("fjord-not-a-password"), bcryptCost)
	return h
})

func (a *authStore) checkPassword(user, password string) bool {
	f, err := a.read()
	if err != nil || f.Hash == "" {
		bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(user)), []byte(f.User)) == 1
	passOK := bcrypt.CompareHashAndPassword([]byte(f.Hash), []byte(password)) == nil
	return userOK && passOK
}

func sessionID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newSession starts a session and returns its cookie value. A sign-in from
// an address never seen before is noted in the activity.
func (a *authStore) newSession(browser, addr string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	now := a.now()
	err := a.change(func(f *authFile) error {
		if f.Sessions == nil {
			f.Sessions = map[string]*authSession{}
		}
		for id, s := range f.Sessions {
			if now.Sub(s.LastUsed) > sessionTTL {
				delete(f.Sessions, id)
			}
		}
		f.Sessions[sessionID(token)] = &authSession{Browser: trimBrowser(browser), Addr: addr, Created: now, LastUsed: now}
		if !slices.Contains(f.SeenAddrs, addr) {
			if len(f.SeenAddrs) > 0 {
				f.addEvent(authEvent{Time: now, Kind: "new-address", Addr: addr, Text: "Signed in from an address not seen before"})
			}
			f.SeenAddrs = append(f.SeenAddrs, addr)
			if n := len(f.SeenAddrs); n > seenAddrsKeep {
				f.SeenAddrs = f.SeenAddrs[n-seenAddrsKeep:]
			}
		}
		return nil
	})
	return token, err
}

// session returns the session id for a cookie value, renewing it at most
// once an hour; "" when it is unknown or expired.
func (a *authStore) session(token string) string {
	if token == "" {
		return ""
	}
	id := sessionID(token)
	f, err := a.read()
	if err != nil {
		return ""
	}
	s := f.Sessions[id]
	now := a.now()
	if s == nil || now.Sub(s.LastUsed) > sessionTTL {
		return ""
	}
	if now.Sub(s.LastUsed) > sessionTouch {
		a.change(func(f *authFile) error {
			if s := f.Sessions[id]; s != nil {
				s.LastUsed = now
			}
			return nil
		})
	}
	return id
}

// endSessions ends the given sessions; all of them except keep when ids is nil.
func (a *authStore) endSessions(ids []string, keep string) error {
	return a.change(func(f *authFile) error {
		for id := range f.Sessions {
			if (ids == nil && id != keep) || slices.Contains(ids, id) {
				delete(f.Sessions, id)
			}
		}
		return nil
	})
}

func (a *authStore) note(e authEvent) {
	a.change(func(f *authFile) error {
		f.addEvent(e)
		return nil
	})
}

func (f *authFile) addEvent(e authEvent) {
	f.Activity = append(f.Activity, e)
	if n := len(f.Activity); n > activityKeep {
		f.Activity = f.Activity[n-activityKeep:]
	}
}

func trimBrowser(ua string) string {
	if len(ua) > 200 {
		ua = ua[:200]
	}
	return ua
}

// Setup code: lets the first account be made only by someone who can read
// a root-only file on the host. Kept until used; survives restarts.

const setupCodeChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O, 1/I

func (a *authStore) ensureSetupCode() (string, error) {
	p := a.setupCodePath()
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	code, err := newSetupCode()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(code+"\n"), 0o600); err != nil {
		return "", err
	}
	return code, nil
}

func newSetupCode() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(setupCodeChars[int(c)%len(setupCodeChars)]) // 256 = 8*32: no bias
	}
	return sb.String(), nil
}

func normalizeCode(s string) string {
	return strings.NewReplacer("-", "", " ", "").Replace(strings.ToUpper(strings.TrimSpace(s)))
}

func (a *authStore) checkSetupCode(code string) bool {
	b, err := os.ReadFile(a.setupCodePath())
	if err != nil {
		return false
	}
	want := normalizeCode(string(b))
	return want != "" && subtle.ConstantTimeCompare([]byte(normalizeCode(code)), []byte(want)) == 1
}

func (a *authStore) removeSetupCode() {
	os.Remove(a.setupCodePath())
}
