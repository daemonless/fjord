package podman

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/daemonless/fjord/pkg/stack"
)

// podman-compose waits for each depends_on service with
//
//	while True: podman wait --condition=running <dep>; sleep 1
//
// and no timeout (1.5.0 here; 1.6.0 and upstream main the same). A
// dependency that exits, or never starts, is never "running", so the up
// never ends: the action spins, the stack stays locked (Delete answers 409),
// and the dependency's own error is never shown. Reproduced on saturn
// 2026-10-04: db exiting 1, app left "Created", podman-compose still in
// `podman wait` a minute later.
//
// So the up is watched. It is stuck when, for depWaitGrace, a container of
// the stack has failed AND podman-compose has printed nothing. Both: a
// stopped stack's containers read "exited" until compose starts them, and a
// slow start is quiet but has nothing failed in it.
var depWaitGrace = 20 * time.Second

// errDependencyFailed is an up stopped because a service it waits on failed.
// What failed has been written to the stream already.
var errDependencyFailed = errors.New("a service the stack waits on failed")

// failedContainer is one container of the stack that has failed.
type failedContainer struct {
	id, service string
	why         string // "exited with code 1", or podman's start error
}

// depWatch decides when an up is stuck, from what each poll sees.
type depWatch struct {
	grace time.Duration
	since map[string]time.Time // container id -> first poll it was seen failed, this run
}

// observe takes one poll: the containers failed now and when compose last
// printed. It returns the ones failed for the whole grace while compose was
// quiet for it too, nil while there is no reason to stop.
func (d *depWatch) observe(now time.Time, failed []failedContainer, lastOutput time.Time) []failedContainer {
	if d.since == nil {
		d.since = map[string]time.Time{}
	}
	still := map[string]time.Time{}
	for _, f := range failed {
		t, ok := d.since[f.id]
		if !ok {
			t = now
		}
		still[f.id] = t
	}
	d.since = still // one that ran in between starts over
	if now.Sub(lastOutput) < d.grace {
		return nil
	}
	var stuck []failedContainer
	for _, f := range failed {
		if now.Sub(d.since[f.id]) >= d.grace {
			stuck = append(stuck, f)
		}
	}
	return stuck
}

// stampedWriter notes when the command last wrote, and when anything did
// (the command or a fjord notice): the stuck-dependency watch needs the
// command's own silence, which the notices must not hide.
type stampedWriter struct {
	w       io.Writer
	mu      sync.Mutex
	last    time.Time // any line
	lastCmd time.Time // the command's own output
}

// Write holds the lock across the write, so a fjord notice never lands in the
// middle of a line the command is writing.
func (s *stampedWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last, s.lastCmd = time.Now(), time.Now()
	return s.w.Write(p)
}

// note writes a fjord line without counting it as the command's output.
func (s *stampedWriter) note(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = time.Now()
	io.WriteString(s.w, line)
}

func (s *stampedWriter) lastWrite() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *stampedWriter) lastCmdWrite() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastCmd
}

// composeUp runs `podman-compose <args>` like runStreaming, stopping it with
// errDependencyFailed when it is stuck waiting on a service that failed.
func (b *Backend) composeUp(ctx context.Context, w io.Writer, s *stack.Stack, args ...string) error {
	fmt.Fprintf(w, "$ podman-compose %s\n", strings.Join(args, " "))
	out := &stampedWriter{w: w, last: time.Now(), lastCmd: time.Now()}
	cmd := exec.CommandContext(ctx, "podman-compose", args...)
	cmd.Dir = s.Dir
	cmd.Stdout, cmd.Stderr = out, out
	// Its own process group, stopped as one: killing podman-compose alone
	// leaves its `podman wait` running forever.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	stop := make(chan struct{})
	var stuck []failedContainer
	var mu sync.Mutex
	if err := cmd.Start(); err != nil {
		return err
	}
	stopNotice := quietNotice(out, "waiting on podman-compose (it prints nothing while a container starts)")
	defer stopNotice()
	go func() {
		watch := &depWatch{grace: depWaitGrace}
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			got := watch.observe(time.Now(), b.failedContainers(ctx, s), out.lastCmdWrite())
			if len(got) == 0 {
				continue
			}
			mu.Lock()
			stuck = got
			mu.Unlock()
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // it is only waiting
			return
		}
	}()
	err := cmd.Wait()
	close(stop)
	mu.Lock()
	defer mu.Unlock()
	if len(stuck) == 0 {
		return err
	}
	// The first [error] line is what the stack page keeps as the reason.
	for _, f := range stuck {
		fmt.Fprintf(w, "\n[error] %s %s; the services that need it were never started\n", f.service, f.why)
		if logs := b.lastLogLines(ctx, f.id, 8); logs != "" {
			fmt.Fprintf(w, "--- last lines from %s ---\n%s\n", f.service, logs)
		}
	}
	fmt.Fprintf(w, "[fjord] podman-compose waits for a service like that forever; stopped it after %s\n", depWaitGrace)
	return errDependencyFailed
}

// failedContainers lists the stack's containers that have failed: exited
// with an error, or never started because podman could not start them. A
// poll that cannot reach podman reports nothing rather than guessing.
func (b *Backend) failedContainers(ctx context.Context, s *stack.Stack) []failedContainer {
	cs, err := b.listStackContainers(ctx, s.Name)
	if err != nil {
		return nil
	}
	var out []failedContainer
	for _, c := range cs {
		if c.State != "exited" && c.State != "stopped" && c.State != "created" {
			continue
		}
		h, err := b.inspectHealth(ctx, c.ID)
		if err != nil {
			continue
		}
		svc := c.Labels["io.podman.compose.service"]
		if svc == "" && len(c.Names) > 0 {
			svc = c.Names[0]
		}
		switch {
		case h.State.Error != "":
			out = append(out, failedContainer{c.ID, svc, "could not start: " + h.State.Error})
		case c.State != "created" && h.State.ExitCode != 0:
			out = append(out, failedContainer{c.ID, svc, fmt.Sprintf("exited with code %d", h.State.ExitCode)})
		}
	}
	return out
}

// lastLogLines is a container's last n log lines, "" when it has none.
func (b *Backend) lastLogLines(ctx context.Context, id string, n int) string {
	out, _ := exec.CommandContext(ctx, "podman", "logs", "--tail", fmt.Sprint(n), id).CombinedOutput()
	return strings.TrimRight(string(out), "\n")
}
