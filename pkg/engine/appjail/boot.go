package appjail

import (
	"context"
	"errors"
	"log"
	"os/exec"
	"time"

	"github.com/daemonless/fjord/pkg/hostexec"
)

var execRuns = hostexec.Default

// appjail's rc service starts every jail marked boot=on with `appjail startup
// start jails`, under daemon(8), so it returns at once and the jails come up in
// the background -- in parallel, the command waiting for each priority group
// before it exits. fjordd's rc script requires appjail, but that only orders it
// after the daemon was spawned. Start on boot then saw a jail still being
// started as down, ran `appjail-director up` for it, and lost the race to
// appjail's own start: "The <jail> jail has been locked", exit 73. The jail
// came up anyway, but the stack was left showing a failed start on boot.

// bootStartPattern is appjail's boot start as ps shows it: the appjail shell
// script, run by sh, with `startup start`. Anchored, so an editor or a pager
// with those words in a file name does not count.
const bootStartPattern = `^(/bin/sh )?[^ ]*/appjail startup start( |$)`

// pgrepRun runs pgrep; a variable so tests can stand in for it.
var pgrepRun = func(args ...string) error { return execRuns.Run(context.Background(), "pgrep", args...) }

// bootStartRunning reports that appjail's own boot start is still running on
// the host (-j none: not a nested appjail inside a jail).
var bootStartRunning = func() bool {
	err := pgrepRun("-q", "-j", "none", "-f", bootStartPattern)
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false // no match: not running
	}
	// pgrep missing or failing: there is nothing to wait on, but say so --
	// otherwise the race this guards against comes back without a trace.
	log.Printf("appjail: cannot tell whether its boot start is running: %v", err)
	return false
}

// bootSettlePoll is how often BootSettle looks again.
var bootSettlePoll = 2 * time.Second

// BootSettle waits for appjail's own boot start to finish (engine.BootSettler).
func (b *Backend) BootSettle(ctx context.Context) error {
	for bootStartRunning() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(bootSettlePoll):
		}
	}
	return nil
}
