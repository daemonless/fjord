package hostexec

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOutputAndRun(t *testing.T) {
	ctx := context.Background()
	out, err := Default.Output(ctx, "sh", "-c", "printf hello")
	if err != nil || string(out) != "hello" {
		t.Fatalf("Output = %q, %v", out, err)
	}
	if err := Default.Run(ctx, "sh", "-c", "exit 0"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestKillGroupAndWaitDelayStopsGrandchildHoldingPipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	// Grandchild sleeps while holding stdout open; KillGroup + WaitDelay must
	// return promptly when ctx expires instead of waiting 30s.
	err := Default.RunOpts(ctx, Options{
		KillGroup: true,
		WaitDelay: 100 * time.Millisecond,
	}, "sh", "-c", "(sleep 30) & wait")
	if err == nil {
		t.Fatal("expected context deadline error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("took %s to return after cancel; grandchild pipe hung", elapsed)
	}
}

func TestCombinedOutputCapturesStderr(t *testing.T) {
	out, err := Default.CombinedOutput(context.Background(), "sh", "-c", "echo oops >&2; exit 1")
	if err == nil || !strings.Contains(string(out), "oops") {
		t.Fatalf("CombinedOutput = %q, %v", out, err)
	}
}
