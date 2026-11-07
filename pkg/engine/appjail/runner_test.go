package appjail

import (
	"context"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/hostexec"
)

type scriptedRunner struct {
	calls []string
	fn    func(name string, args ...string) ([]byte, error)
}

func (s *scriptedRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	s.calls = append(s.calls, name+" "+strings.Join(args, " "))
	return s.fn(name, args...)
}

func (s *scriptedRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return s.Output(ctx, name, args...)
}

func (s *scriptedRunner) Run(ctx context.Context, name string, args ...string) error {
	_, err := s.Output(ctx, name, args...)
	return err
}

func (s *scriptedRunner) RunOpts(ctx context.Context, _ hostexec.Options, name string, args ...string) error {
	return s.Run(ctx, name, args...)
}

func TestBootStartRunningUsesHostexecRunner(t *testing.T) {
	origExec := execRuns
	t.Cleanup(func() { execRuns = origExec })

	sr := &scriptedRunner{
		fn: func(name string, args ...string) ([]byte, error) {
			return nil, nil
		},
	}
	execRuns = sr
	if !bootStartRunning() {
		t.Fatal("expected bootStartRunning() = true when pgrep exits 0")
	}
	if len(sr.calls) != 1 || !strings.HasPrefix(sr.calls[0], "pgrep ") {
		t.Fatalf("unexpected calls: %v", sr.calls)
	}
}
