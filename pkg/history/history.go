// Package history keeps what happened to each stack: one line per action
// (install, update, rollback, start, stop, delete, a recovery), who or what
// started it, and how it ended. It lives under fjord's root, not in the
// stack's directory, so a deleted stack's last record survives.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/stack"
)

// Actions.
const (
	Install  = "install"
	Adopt    = "adopt"
	Start    = "start"
	Stop     = "stop"
	Restart  = "restart"
	Apply    = "apply"
	Update   = "update"
	Rollback = "rollback"
	Snapshot = "snapshot"
	Delete   = "delete"
	Recovery = "recovery"
)

// Triggers: who or what started an action.
const (
	ByYou       = "you"
	ByCLI       = "cli"
	ByBoot      = "boot"
	ByRecovery  = "recovery"
	ByAutomatic = "automatic"
)

// Results.
const (
	OK     = "ok"
	Failed = "failed"
)

// Keep is how many entries a stack keeps; older ones are dropped on write.
const Keep = 200

// Image is one service's image before or after an action.
type Image struct {
	Ref    string `json:"ref,omitempty"`
	Digest string `json:"digest,omitempty"`
}

// Change is what an update or rollback did to one service.
type Change struct {
	Service string `json:"service"`
	From    *Image `json:"from,omitempty"`
	To      *Image `json:"to,omitempty"`
}

// Entry is one action on one stack.
type Entry struct {
	At       time.Time `json:"at"`
	Stack    string    `json:"stack"`
	Action   string    `json:"action"`
	Trigger  string    `json:"trigger"`
	Result   string    `json:"result"`
	Message  string    `json:"message,omitempty"`
	Services []string  `json:"services,omitempty"`
	Changes  []Change  `json:"changes,omitempty"`
}

// Log is the history directory under fjord's root.
type Log struct {
	dir string
	mu  sync.Mutex
}

// New returns the history kept in <root>/history.
func New(root string) *Log { return &Log{dir: filepath.Join(root, "history")} }

func (l *Log) path(name string) (string, bool) {
	if !stack.ValidName(name) {
		return "", false
	}
	return filepath.Join(l.dir, name+".jsonl"), true
}

// Append adds e to its stack's history, dropping the oldest entries beyond
// Keep. Best effort for the caller: an error is returned, never fatal.
func (l *Log) Append(e Entry) error {
	p, ok := l.path(e.Stack)
	if !ok {
		return os.ErrInvalid
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	// A line a crash cut short gets its own line ended first, so it stays
	// one unreadable line instead of spoiling this one.
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		last := make([]byte, 1)
		if r, err := os.Open(p); err == nil {
			if _, err := r.ReadAt(last, st.Size()-1); err == nil && last[0] != '\n' {
				line = append([]byte{'\n'}, line...)
			}
			r.Close()
		}
	}
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	return l.trim(p)
}

// trim rewrites p with its last Keep lines once it holds a quarter more,
// so most appends stay a single write.
func (l *Log) trim(p string) error {
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= Keep+Keep/4 {
		return nil
	}
	return stack.WriteFileAtomic(p, bytes.Join(lines[len(lines)-Keep:], nil), 0o600)
}

// Stack returns name's entries, newest first, at most limit (0 = all kept).
func (l *Log) Stack(name string, limit int) []Entry {
	p, ok := l.path(name)
	if !ok {
		return nil
	}
	l.mu.Lock()
	es := read(p)
	l.mu.Unlock()
	return newest(es, limit)
}

// All returns every stack's entries, deleted stacks included, newest first,
// at most limit (0 = all kept).
func (l *Log) All(limit int) []Entry {
	l.mu.Lock()
	files, _ := filepath.Glob(filepath.Join(l.dir, "*.jsonl"))
	var es []Entry
	for _, f := range files {
		es = append(es, read(f)...)
	}
	l.mu.Unlock()
	return newest(es, limit)
}

func read(p string) []Entry {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	var es []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e Entry
		// A torn last line (a crash mid-write) is skipped, not fatal.
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Action != "" {
			es = append(es, e)
		}
	}
	return es
}

func newest(es []Entry, limit int) []Entry {
	sort.SliceStable(es, func(i, j int) bool { return es[i].At.After(es[j].At) })
	if limit > 0 && len(es) > limit {
		es = es[:limit]
	}
	return es
}

// FirstLine is msg cut to its first line, for Entry.Message.
func FirstLine(msg string) string {
	msg = strings.TrimSpace(msg)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return msg
}
