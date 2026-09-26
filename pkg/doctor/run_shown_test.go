package doctor

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// The setup page's terminal: each command echoed like a shell prompt, then
// its output; a failure names the command that failed.
func TestRunShownEchoesThenRuns(t *testing.T) {
	var out bytes.Buffer
	if err := runShown(context.Background(), &out, nil, "echo", "hello"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "$ echo hello\nhello\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	out.Reset()
	err := runShown(context.Background(), &out, nil, "sh", "-c", "echo broken >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "sh -c") {
		t.Fatalf("error should name the command, got %v", err)
	}
	if !strings.Contains(out.String(), "broken") {
		t.Fatalf("stderr should reach the terminal, got %q", out.String())
	}
}

// A service that daemonizes keeps the output pipe open after `service` exits
// 0; that must not hang the setup page.
func TestRunShownDoesNotWaitForADaemonsOutput(t *testing.T) {
	outputDrain = 200 * time.Millisecond
	defer func() { outputDrain = 3 * time.Second }()
	var out bytes.Buffer
	start := time.Now()
	if err := runShown(context.Background(), &out, nil, "sh", "-c", "sleep 30 & echo started"); err != nil {
		t.Fatalf("a command that exited 0 is a success: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("waited %s for a background child's output", d)
	}
	if !strings.Contains(out.String(), "started") {
		t.Fatalf("output before the exit should still arrive, got %q", out.String())
	}
}
