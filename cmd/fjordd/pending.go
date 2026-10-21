package main

import (
	"io"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

// recordPending adds the services a Save changed to the stack's pending list,
// for the next start or Apply to recreate.
func (s *server) recordPending(name string, changed []string) {
	if len(changed) == 0 {
		return
	}
	err := s.manager.UpdateState(name, func(st *stack.State) *stack.State {
		if st == nil {
			return nil
		}
		for _, svc := range changed {
			if !slices.Contains(st.PendingServices, svc) {
				st.PendingServices = append(st.PendingServices, svc)
			}
		}
		slices.Sort(st.PendingServices)
		return st
	})
	if err != nil {
		log.Printf("save %s: record changed services: %v", name, err)
	}
}

// pendingFor is the stack's pending list, limited to services it still has:
// one removed since is gone with the next up, not something to recreate.
func (s *server) pendingFor(st *stack.Stack) []string {
	state, err := s.manager.LoadState(st.Name)
	if err != nil || state == nil {
		return nil
	}
	have := map[string]bool{}
	for _, svc := range composepkg.ParseServices(st.Compose, st.EnvMap()) {
		have[svc.Name] = true
	}
	var out []string
	for _, svc := range state.PendingServices {
		if have[svc] {
			out = append(out, svc)
		}
	}
	return out
}

// clearPendingAfter passes stream through and takes done off the pending
// list (nil = all of it) once the containers carry the changes: when the
// stay-up check starts, or the stream ends, with no [error] before it. A
// failed recreate leaves them pending, so the banner keeps saying the changes
// are not applied. Not only at the stream's end: the stack reads as idle from
// the check's start (watchable), and a page closed during the check never
// read to the end, which left the banner up for good.
func (s *server) clearPendingAfter(name string, done []string, stream io.ReadCloser) io.ReadCloser {
	watch := &errorWatch{}
	var once sync.Once
	clear := func() {
		once.Do(func() {
			err := s.manager.UpdateState(name, func(st *stack.State) *stack.State {
				if st == nil || len(st.PendingServices) == 0 {
					return nil
				}
				if done == nil {
					st.PendingServices = nil
				} else {
					st.PendingServices = slices.DeleteFunc(st.PendingServices, func(svc string) bool { return slices.Contains(done, svc) })
				}
				return st
			})
			if err != nil {
				log.Printf("%s: clear applied changes: %v", name, err)
			}
		})
	}
	check := &lineWatch{prefix: engine.WatchStartPrefix, seen: clear}
	return &thenReader{r: io.TeeReader(io.TeeReader(stream, watch), check), c: stream, after: func() string {
		if !watch.seen {
			clear()
		}
		return ""
	}}
}

// lineWatch calls seen the first time a line starts with prefix, unless an
// [error] line came first. Line by line, so an [error] after the prefix in
// the same chunk does not count against it.
type lineWatch struct {
	prefix string
	seen   func()
	line   []byte // the current line so far, up to len(prefix)
	done   bool
}

func (l *lineWatch) Write(p []byte) (int, error) {
	for _, c := range p {
		if l.done {
			break
		}
		if c == '\n' {
			l.line = l.line[:0]
			continue
		}
		if len(l.line) >= len(l.prefix) {
			continue
		}
		l.line = append(l.line, c)
		switch {
		case string(l.line) == "[error]":
			l.done = true
		case string(l.line) == l.prefix:
			l.done = true
			l.seen()
		}
	}
	return len(p), nil
}

// errorWatch notices an "[error]" line in a stream, across chunk boundaries,
// and keeps the first one.
type errorWatch struct {
	tail []byte // the unfinished line so far, once an [error] is seen the line itself
	seen bool
	line string
}

func (e *errorWatch) Write(p []byte) (int, error) {
	if e.line != "" {
		return len(p), nil
	}
	buf := append(e.tail, p...)
	if !e.seen {
		if i := strings.Index(string(buf), "[error]"); i >= 0 {
			e.seen = true
			buf = buf[i:]
		} else if len(buf) > 8 {
			buf = buf[len(buf)-8:]
		}
	}
	if e.seen {
		if nl := strings.IndexByte(string(buf), '\n'); nl >= 0 {
			e.line = strings.TrimSpace(strings.TrimPrefix(string(buf[:nl]), "[error]"))
			buf = nil
		} else if len(buf) > 512 {
			e.line = strings.TrimSpace(strings.TrimPrefix(string(buf[:512]), "[error]"))
			buf = nil
		}
	}
	e.tail = append([]byte(nil), buf...)
	return len(p), nil
}

// Message is the first [error] line seen, without its tag; "" when none.
func (e *errorWatch) Message() string {
	if e.line != "" {
		return e.line
	}
	if e.seen {
		return strings.TrimSpace(strings.TrimPrefix(string(e.tail), "[error]"))
	}
	return ""
}

// recordOutcome passes stream through and, once it ends, writes the outcome
// on the stack: an [error] becomes LastFailure (and a log line), a clean end
// clears one. The stream is what the page shows; this is what stays once
// the page is gone.
func (s *server) recordOutcome(name, action string, stream io.ReadCloser) io.ReadCloser {
	watch := &errorWatch{}
	kept := outputs.current(name, action)
	return &thenReader{r: io.TeeReader(io.TeeReader(stream, watch), kept), c: stream, after: func() string {
		kept.finish()
		if watch.seen {
			log.Printf("%s %s failed: %s", action, name, watch.Message())
		}
		err := s.manager.UpdateState(name, func(st *stack.State) *stack.State {
			if st == nil || (!watch.seen && st.LastFailure == nil) {
				return nil
			}
			if watch.seen {
				st.LastFailure = &stack.Failure{Action: action, At: time.Now(), Message: watch.Message()}
			} else {
				st.LastFailure = nil
			}
			return st
		})
		if err != nil {
			log.Printf("%s %s: record outcome: %v", action, name, err)
		}
		return ""
	}}
}

// thenStream reads first to its end, then starts next and reads that: the two
// run one after the other, never together.
func thenStream(first io.ReadCloser, next func() (io.ReadCloser, error)) io.ReadCloser {
	return &seqReader{cur: first, next: next}
}

type seqReader struct {
	cur  io.ReadCloser
	next func() (io.ReadCloser, error)
}

func (q *seqReader) Read(p []byte) (int, error) {
	n, err := q.cur.Read(p)
	if err == io.EOF && q.next != nil {
		q.cur.Close()
		nxt, nerr := q.next()
		q.next = nil
		if nerr != nil {
			q.cur = io.NopCloser(strings.NewReader("\n[error] " + nerr.Error() + "\n"))
		} else {
			q.cur = nxt
		}
		if n > 0 {
			return n, nil
		}
		return q.Read(p)
	}
	return n, err
}

func (q *seqReader) Close() error { return q.cur.Close() }
