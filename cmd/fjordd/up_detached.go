package main

import (
	"context"
	"io"
	"log"
	"strings"
	"time"
)

// upDetached brings a stack up with nobody waiting on the stream: locked
// like any action, so the page shows it starting and a Start pressed
// meanwhile is refused instead of racing it; its output kept for any page
// that looks; its outcome recorded as the stack's failure or not. For a
// start that a request set in motion and that must not depend on the page
// that made it: Adopt with replace removed the running container in that
// request, and the start used to be a second call from the page.
func (s *server) upDetached(name, action string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		st, err := s.manager.Get(name)
		if err != nil {
			log.Printf("%s %s: %v", action, name, err)
			return
		}
		unlock, ok := lockStackAs(name, "up")
		if !ok {
			log.Printf("%s %s: %s", action, name, anotherOperation(name))
			return
		}
		defer unlock()
		stream, err := s.backendFor(st).Up(watchable(ctx, name), st)
		if err != nil {
			stream = io.NopCloser(strings.NewReader("[error] " + err.Error() + "\n"))
		} else if chose := s.chooseAddresses(ctx, st); chose != "" {
			stream = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(strings.NewReader(chose), stream), stream}
		}
		io.Copy(io.Discard, s.recordOutcome(name, action, s.keepAfter(ctx, st, stream)))
		stream.Close()
	}()
}

// replacedWasRunning says an adopt with replace took a running container:
// the stack it became must run too, by fjord's doing, not the page's.
func replacedWasRunning(replace bool, state string) bool {
	return replace && state == "running"
}
