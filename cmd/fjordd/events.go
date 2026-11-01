package main

import (
	"encoding/json"
	"fmt"
	"github.com/daemonless/fjord/pkg/engine"
	"net/http"
	"sort"
	"strings"
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
	idle := 0                   // polls skipped while nobody watches
	for {
		time.Sleep(1500 * time.Millisecond)
		watched := s.events.count() > 0
		if !watched {
			// Nobody watching: skip the (shell-out) status calls, unless
			// names are in play. A port changed by Apply from the API, or a
			// lease that moved at night, left a name pointing at nothing
			// until someone opened fjord (and then only on the next change).
			// So with a provider the loop still looks, every 30 s.
			idle++
			if idle < 20 || !s.namesInPlay() {
				continue
			}
		}
		idle = 0
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
			// The published ports are part of the key: a port changed in
			// .env and applied recreates the container too fast for the
			// poll to see it stop, and the name kept pointing at the old
			// port (a 503 behind the name).
			key := fmt.Sprintf("%s|%s|%t|%s", state, link, !status.StaleSince.IsZero(), publishedPorts(status))
			if last[full.Name] == key {
				continue
			}
			// On the first look too: fjordd was down, or nobody watched,
			// while the app moved. syncStack writes only when a name no
			// longer matches, so a quiet start costs one read.
			if state == "running" {
				s.syncTargets(full, status)
			}
			if !watched {
				last[full.Name] = key
				continue // nothing to publish to
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

// publishedPorts is every host port a stack's containers publish, sorted,
// so a change of port is a change of state for the watchers.
func publishedPorts(status engine.StackStatus) string {
	var ports []string
	for _, c := range status.Containers {
		for _, p := range c.Ports {
			ports = append(ports, fmt.Sprintf("%d:%d", p.HostPort, p.ContainerPort))
		}
	}
	sort.Strings(ports)
	return strings.Join(ports, ",")
}
