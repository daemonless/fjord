// Package hostexec runs host commands with opt-in process-group termination
// and WaitDelay so grandchild processes holding stdio pipes cannot hang a
// cancelled command forever, while keeping process-group kill opt-in per call
// site so cancelling a recreate does not kill running jails or containers left
// in the group.
package hostexec

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// Options configures a single command invocation.
type Options struct {
	Dir       string
	Env       []string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	KillGroup bool
	WaitDelay time.Duration
}

// Runner executes external host commands and can be swapped in unit tests.
type Runner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error)
	Run(ctx context.Context, name string, args ...string) error
	RunOpts(ctx context.Context, opts Options, name string, args ...string) error
}

// Default is the standard OS command runner.
var Default Runner = osRunner{}

type osRunner struct{}

const defaultWaitDelay = 3 * time.Second

func (osRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	err := (osRunner{}).RunOpts(ctx, Options{Stdout: &stdout}, name, args...)
	return stdout.Bytes(), err
}

func (osRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	var buf bytes.Buffer
	err := (osRunner{}).RunOpts(ctx, Options{Stdout: &buf, Stderr: &buf}, name, args...)
	return buf.Bytes(), err
}

func (osRunner) Run(ctx context.Context, name string, args ...string) error {
	return (osRunner{}).RunOpts(ctx, Options{}, name, args...)
}

func (osRunner) RunOpts(ctx context.Context, opts Options, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = opts.Dir
	cmd.Env = opts.Env
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	if opts.WaitDelay > 0 {
		cmd.WaitDelay = opts.WaitDelay
	} else {
		cmd.WaitDelay = defaultWaitDelay
	}
	if opts.KillGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return nil
			}
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	return cmd.Run()
}
