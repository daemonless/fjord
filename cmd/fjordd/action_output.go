package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// actionOutput keeps the output of each stack's running or last action. The
// stream goes to the page that started the action and to nothing else; a
// page opened later, or the same one after a reload, showed "Waiting for
// output…" under a greyed Start with no way to tell what was happening
// (code-server on army, 2026-10-01). Per stack, bounded, in memory: the
// History tab (0.4) is where it goes on disk.
type actionOutput struct {
	mu      sync.Mutex
	byStack map[string]*outputBuf
}

// outputKeep is how much of an action's output is kept, from the end.
const outputKeep = 256 << 10

type outputBuf struct {
	mu      sync.Mutex
	Action  string
	Started time.Time
	Done    bool
	buf     []byte
}

// outputs is the one store: the lock that marks a stack busy starts its
// buffer, so a page asking in the seconds before the engine's stream exists
// is told "running, nothing yet" rather than "done, nothing".
var outputs actionOutput

// start begins a new buffer for an action on a stack, replacing the last.
func (a *actionOutput) start(name, action string) *outputBuf {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byStack == nil {
		a.byStack = map[string]*outputBuf{}
	}
	b := &outputBuf{Action: action, Started: time.Now()}
	a.byStack[name] = b
	return b
}

// current is the stack's running buffer, or a new one for action when the
// action was not taken under a lock (install, which locks by id itself).
func (a *actionOutput) current(name, action string) *outputBuf {
	a.mu.Lock()
	b := a.byStack[name]
	a.mu.Unlock()
	if b != nil && !b.Done {
		return b
	}
	return a.start(name, action)
}

// finish marks the stack's running buffer done, if any.
func (a *actionOutput) finish(name string) {
	a.mu.Lock()
	b := a.byStack[name]
	a.mu.Unlock()
	if b != nil {
		b.finish()
	}
}

// Write appends, keeping the last outputKeep bytes cut at a line start.
func (b *outputBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > outputKeep {
		cut := len(b.buf) - outputKeep
		for cut < len(b.buf) && b.buf[cut] != '\n' {
			cut++
		}
		if cut < len(b.buf) {
			cut++ // past the newline: the kept text starts at a line
		}
		b.buf = append([]byte(nil), b.buf[cut:]...)
	}
	return len(p), nil
}

func (b *outputBuf) finish() {
	b.mu.Lock()
	b.Done = true
	b.mu.Unlock()
}

// outputView is what the page gets: GET /api/stacks/<name>/output.
type outputView struct {
	Action  string    `json:"action"`
	Started time.Time `json:"started"`
	Done    bool      `json:"done"`
	Text    string    `json:"text"`
}

func (a *actionOutput) get(name string) (outputView, bool) {
	a.mu.Lock()
	b := a.byStack[name]
	a.mu.Unlock()
	if b == nil {
		return outputView{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return outputView{Action: b.Action, Started: b.Started, Done: b.Done, Text: string(b.buf)}, true
}

// stackOutput serves a stack's running or last action output.
func (s *server) stackOutput(w http.ResponseWriter, name string) {
	v, ok := outputs.get(name)
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		w.Write([]byte(`{"action":"","done":true,"text":""}`))
		return
	}
	json.NewEncoder(w).Encode(v)
}
