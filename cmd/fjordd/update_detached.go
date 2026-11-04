package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"slices"
	"strings"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/stack"
)

// updateStream is an update as the daemon runs it, whoever asked: the
// rollback record taken (not for a rollback, which must keep the one that
// worked), the addresses it keeps said first, the engine's pull and
// recreate, and the changes a Save left pending cleared as they land.
// services nil is the whole stack.
func (s *server) updateStream(ctx context.Context, st *stack.Stack, read uint64, services []string, record bool) (io.ReadCloser, error) {
	if record {
		s.recordRollback(ctx, st, services)
	}
	note := keptNote(s.keepAddresses(ctx, st))
	stream, err := s.backendFor(st).Update(watchable(ctx, st.Name), st, services)
	if err != nil {
		return nil, err
	}
	// An update recreates what it touches, from the saved config.
	stream = s.clearPendingAfter(st.Name, services, read, stream)
	if note != "" {
		stream = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(strings.NewReader(note), stream), stream}
	}
	return stream, nil
}

// updateDetached runs an update with nobody waiting on the stream, under
// the lock the caller took as lockStackAs(name, "update") and hands over
// here: the compose it applies is the one written under that same lock.
// Its output is kept for any page that looks, its outcome recorded as the
// stack's failure or not, the fleet's verdict on the stack forgotten. For
// an update a request set in motion and that must not depend on the page
// that made it: a version change used to be set-tag from the page and then
// the update as a second call, and a page gone between the two left the
// compose naming a version the stack did not run.
func (s *server) updateDetached(name string, unlock func(), services []string) {
	go func() {
		defer unlock()
		s.runDetachedUpdate(name, services)
	}()
}

// runDetachedUpdate runs one stack's update under the caller's lock, keeps
// its output, records its outcome, and forgets the fleet's verdict on it.
func (s *server) runDetachedUpdate(name string, services []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	read := s.pending.now()
	st, err := s.manager.Get(name)
	if err != nil {
		log.Printf("update %s: %v", name, err)
		return
	}
	log.Printf("update %s (%s)", name, st.EngineName())
	var stream io.ReadCloser
	if problems := s.preflight(ctx, st); len(problems) > 0 {
		stream = errorStream("pre-flight failed:\n  " + strings.Join(problems, "\n  "))
	} else if stream, err = s.updateStream(ctx, st, read, services, true); err != nil {
		stream = errorStream(err.Error())
	} else {
		stream = s.keepAfter(ctx, st, stream)
	}
	io.Copy(io.Discard, s.recordOutcome(name, "update", stream))
	stream.Close()
	s.fleet.forget(name)
}

// errorStream is an action's output when it could not start: the one line
// the page shows and recordOutcome keeps as the failure.
func errorStream(msg string) io.ReadCloser {
	return io.NopCloser(strings.NewReader("[error] " + msg + "\n"))
}

// knownServices checks that each name is a service of the stack: compose
// would otherwise answer a typo with "no such service" halfway through,
// after the pull. engineKnows covers a stack with no compose to name them
// (a native appjail one), or whose engine names its services differently.
func knownServices(st *stack.Stack, names []string, engineKnows func() []string) error {
	known, _ := composepkg.ServiceImageList(st.Compose)
	var more []string
	for _, want := range names {
		if slices.ContainsFunc(known, func(si composepkg.ServiceImage) bool { return si.Service == want }) {
			continue
		}
		if more == nil && engineKnows != nil {
			more = append([]string{}, engineKnows()...)
		}
		if !slices.Contains(more, want) {
			return fmt.Errorf("%s has no service %q", st.Name, want)
		}
	}
	return nil
}
