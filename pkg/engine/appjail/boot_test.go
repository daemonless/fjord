package appjail

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"testing"
	"time"
)

// Start on boot used to race appjail's own boot start and lose with "has been
// locked", leaving every stack it reached showing a failed start.
func TestBootSettleWaitsForAppjailStartup(t *testing.T) {
	oldRunning, oldPoll := bootStartRunning, bootSettlePoll
	t.Cleanup(func() { bootStartRunning, bootSettlePoll = oldRunning, oldPoll })
	bootSettlePoll = time.Millisecond

	polls := 0
	bootStartRunning = func() bool { polls++; return polls <= 3 }
	if err := NewBackend().BootSettle(context.Background()); err != nil {
		t.Fatalf("settled startup: %v", err)
	}
	if polls != 4 {
		t.Errorf("returned after %d looks, want 4 (3 running, then done)", polls)
	}

	bootStartRunning = func() bool { return true }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := NewBackend().BootSettle(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("startup that never ends: got %v, want the deadline", err)
	}
}

// Only pgrep's "no match" (exit 1) means appjail's boot start is over. A pgrep
// that failed some other way cannot say, and is logged rather than read as
// "finished" in silence.
func TestBootStartRunningReadsPgrepExit(t *testing.T) {
	old := pgrepRun
	t.Cleanup(func() { pgrepRun = old })
	exitWith := func(code string) func(...string) error {
		return func(...string) error { return exec.Command("sh", "-c", "exit "+code).Run() }
	}
	for _, c := range []struct {
		name string
		run  func(...string) error
		want bool
	}{
		{"match", exitWith("0"), true},
		{"no match", exitWith("1"), false},
		{"pgrep error", exitWith("2"), false},
		{"pgrep missing", func(...string) error { return exec.ErrNotFound }, false},
	} {
		pgrepRun = c.run
		if got := bootStartRunning(); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// The pattern must catch appjail's boot start as ps shows it, and nothing that
// merely mentions it.
func TestBootStartPattern(t *testing.T) {
	re := regexp.MustCompile(bootStartPattern)
	for args, want := range map[string]bool{
		"/bin/sh /usr/local/bin/appjail startup start jails": true,
		"/usr/local/bin/appjail startup start":               true,
		"/bin/sh /usr/local/bin/appjail start -- web":        false, // one jail's own start
		"less notes-appjail startup start.txt":               false,
		"/bin/sh /usr/local/bin/appjail startup stop jails":  false,
	} {
		if got := re.MatchString(args); got != want {
			t.Errorf("%q: got %v, want %v", args, got, want)
		}
	}
}
