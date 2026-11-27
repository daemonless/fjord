package main

import (
	"context"
	"io"
	"log"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/history"
)

// upDetached brings a stack up with nobody waiting on the stream: locked
// like any action, so the page shows it starting and a Start pressed
// meanwhile is refused instead of racing it; its output kept for any page
// that looks; its outcome recorded as the stack's failure or not. For a
// start that a request set in motion and that must not depend on the page
// that made it: Adopt with replace removed the running container in that
// request, and the start used to be a second call from the page.
func (s *server) upDetached(name, action, trigger string) {
	go s.upNow(name, action, trigger)
}

// upNow is upDetached's start, waited for: "" when the stack came up, else
// why not. For a job that starts stacks one after another.
func (s *server) upNow(name, action, trigger string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	st, err := s.manager.Get(name)
	if err != nil {
		log.Printf("%s %s: %v", action, name, err)
		return err.Error()
	}
	unlock, ok := lockStackAs(name, "up")
	if !ok {
		log.Printf("%s %s: %s", action, name, anotherOperation(name))
		return anotherOperation(name)
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
	var tail strings.Builder
	io.Copy(&tail, s.recordOutcome(name, action, newNote(history.Start, trigger), s.keepAfter(ctx, st, stream)))
	stream.Close()
	if i := strings.LastIndex(tail.String(), "[error] "); i >= 0 {
		return strings.TrimSpace(strings.SplitN(tail.String()[i+len("[error] "):], "\n", 2)[0])
	}
	return ""
}

// replacedWasRunning says an adopt with replace took a running container:
// the stack it became must run too, by fjord's doing, not the page's.
func replacedWasRunning(replace bool, state string) bool {
	return replace && state == "running"
}
