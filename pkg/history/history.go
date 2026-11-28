// Package history keeps what happened to each stack: one line per action
// (install, update, rollback, start, stop, delete, a recovery), who or what
// started it, and how it ended. It lives under fjord's root, not in the
// stack's directory, so a deleted stack's last record survives.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	Excerpt  string    `json:"excerpt,omitempty"`
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

const (
	// ExcerptHead and ExcerptTail bound the lines kept on a failed History entry.
	ExcerptHead    = 10
	ExcerptTail    = 10
	excerptLineCap = 512
)

var (
	ansiSeqRe      = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	urlCredRe      = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^:@/\s]+):([^@/\s]+)@`)
	authHeaderRe   = regexp.MustCompile(`(?i)(Authorization:\s*(?:Bearer|Basic|Token)\s+)(\S+)`)
	secretAssignRe = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:PASS|PASSWORD|PASSWD|PWD|SECRET|TOKEN|API_KEY|APIKEY|PRIVATE_KEY|CREDENTIAL|AUTH)[A-Z0-9_]*)=("[^"]*"|'[^']*'|\S+)`)
)

func redactExcerptLine(line string) string {
	line = urlCredRe.ReplaceAllString(line, "${1}:[redacted]@")
	line = authHeaderRe.ReplaceAllString(line, "${1}[redacted]")
	line = secretAssignRe.ReplaceAllString(line, "${1}=[redacted]")
	return line
}

func cleanExcerptLine(line string) string {
	line = ansiSeqRe.ReplaceAllString(line, "")
	line = strings.TrimRight(line, " \t\r")
	line = redactExcerptLine(line)
	if len(line) > excerptLineCap {
		line = line[:excerptLineCap] + "..."
	}
	return line
}

// Excerpt keeps up to the first 10 and last 10 lines of raw action output.
// Short outputs (20 lines or fewer) are kept whole; longer ones are cut in the
// middle with a line count marker.
func Excerpt(raw string) string {
	w := NewExcerptWriter()
	_, _ = w.Write([]byte(raw))
	return w.String()
}

// ExcerptWriter streams action output in O(1) memory, retaining only the first
// ExcerptHead lines and a ring of the last ExcerptTail lines.
type ExcerptWriter struct {
	head    []string
	tail    []string
	total   int
	partial bytes.Buffer
}

// NewExcerptWriter returns a streaming writer that builds a failed-entry excerpt.
func NewExcerptWriter() *ExcerptWriter {
	return &ExcerptWriter{}
}

func (w *ExcerptWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\r' || b == '\n' {
			w.pushLine(w.partial.String())
			w.partial.Reset()
			continue
		}
		if w.partial.Len() < excerptLineCap+64 {
			w.partial.WriteByte(b)
		}
	}
	return len(p), nil
}

func (w *ExcerptWriter) pushLine(raw string) {
	line := cleanExcerptLine(raw)
	if line == "" && w.total == 0 {
		return
	}
	w.total++
	if len(w.head) < ExcerptHead {
		w.head = append(w.head, line)
		return
	}
	if len(w.tail) < ExcerptTail {
		w.tail = append(w.tail, line)
		return
	}
	copy(w.tail, w.tail[1:])
	w.tail[len(w.tail)-1] = line
}

// String finishes any trailing unterminated line and returns the formatted excerpt.
func (w *ExcerptWriter) String() string {
	if w.partial.Len() > 0 {
		w.pushLine(w.partial.String())
		w.partial.Reset()
	}
	// Trim trailing blank lines from tail (or head when tail is empty).
	for len(w.tail) > 0 && w.tail[len(w.tail)-1] == "" {
		w.tail = w.tail[:len(w.tail)-1]
		w.total--
	}
	if len(w.tail) == 0 {
		for len(w.head) > 0 && w.head[len(w.head)-1] == "" {
			w.head = w.head[:len(w.head)-1]
			w.total--
		}
	}
	if len(w.head) == 0 {
		return ""
	}
	if w.total <= ExcerptHead+ExcerptTail {
		all := make([]string, 0, len(w.head)+len(w.tail))
		all = append(all, w.head...)
		all = append(all, w.tail...)
		return strings.Join(all, "\n")
	}
	omitted := w.total - len(w.head) - len(w.tail)
	lines := make([]string, 0, len(w.head)+1+len(w.tail))
	lines = append(lines, w.head...)
	lines = append(lines, fmt.Sprintf("... (%d lines omitted) ...", omitted))
	lines = append(lines, w.tail...)
	return strings.Join(lines, "\n")
}
