package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// eventHub is a minimal SSE pub/sub: each subscriber gets a buffered channel,
// and publish fans a message out to all of them. A slow subscriber is dropped
// (skipped) rather than allowed to block the publisher. One server-side status
// loop (runEventLoop) feeds every connected browser, replacing per-tab polling.
type eventHub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newEventHub() *eventHub {
	return &eventHub{subs: map[chan []byte]struct{}{}}
}

func (h *eventHub) subscribe() chan []byte {
	ch := make(chan []byte, 16)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *eventHub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *eventHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *eventHub) publish(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- b:
		default: // slow consumer: drop this message rather than stall the hub
		}
	}
}

// handleEvents streams server events to one browser over SSE. The client opens
// a single EventSource and gets stack state-changes pushed as they happen; the
// baseline comes from its initial /api/stacks fetch, so this carries only
// deltas. GET only (guardMutations lets reads through).
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.events.subscribe()
	defer s.events.unsubscribe(ch)

	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// A comment ping keeps the connection (and any intermediary proxy) open
	// through idle periods.
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case b, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// runEventLoop watches stack status while at least one client is connected and
// publishes only the stacks whose state changed. This moves polling from every
// browser tab to a single server-side loop; it idles cheaply (no engine calls)
// when nobody is watching. Runs for the life of the process.
func (s *server) runEventLoop() {
	last := map[string]string{} // stack name -> last published state
	for {
		time.Sleep(1500 * time.Millisecond)
		if s.events.count() == 0 {
			continue // nobody watching: skip the (shell-out) status calls
		}
		stacks, err := s.manager.List()
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, st := range stacks {
			full, err := s.manager.Get(st.Name)
			if err != nil {
				continue
			}
			seen[full.Name] = true
			// Through liveStatus: a busy engine gives the last state, marked
			// stale, where a timeout here used to publish "unknown" and then
			// the real state again.
			status := s.live.get(s.backendFor(full), full, statusWait)
			state := status.State
			// Where Open goes changes without the state changing: a DHCP
			// lease lands after "running". Sent with the status, or the page
			// kept the empty link it saw mid-recreate and opened 10.89.x.
			link := linkHost(stackServices(full, status), ownAddress)
			key := fmt.Sprintf("%s|%s|%t", state, link, !status.StaleSince.IsZero())
			if last[full.Name] == key {
				continue
			}
			last[full.Name] = key
			if b, err := json.Marshal(map[string]any{
				"type":     "stack",
				"name":     full.Name,
				"state":    state,
				"status":   status,
				"linkHost": link,
			}); err == nil {
				s.events.publish(b)
			}
		}
		// A stack that vanished (deleted): tell clients and forget it.
		for name := range last {
			if !seen[name] {
				delete(last, name)
				s.live.forget(name)
				if b, err := json.Marshal(map[string]any{"type": "stack-removed", "name": name}); err == nil {
					s.events.publish(b)
				}
			}
		}
	}
}
