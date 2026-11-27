package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/stack"
)

// Which buttons the System page gets, by podman's version and backend and
// whether netavark-migrate (with --no-start) is there.
func TestNetSwitchState(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("switching is FreeBSD's")
	}
	tool := filepath.Join(t.TempDir(), "netavark-migrate")
	os.WriteFile(tool, nil, 0o755)
	oldTool, oldRun, oldMaj, oldBack := migrateTool, runMigrate, podmanMajor, podmanBackend
	t.Cleanup(func() { migrateTool, runMigrate, podmanMajor, podmanBackend = oldTool, oldRun, oldMaj, oldBack })
	migrateTool = tool
	version := "netavark-migrate 2 host json no-start\n"
	runMigrate = func(ctx context.Context, args ...string) ([]byte, int, error) { return []byte(version), 0, nil }
	s := &server{manager: stack.NewManager(t.TempDir())}

	cases := []struct {
		name            string
		major           int
		backend         string
		version         string
		running         bool
		canSwitch, back bool
		note            string
	}{
		{"podman 5 on CNI", 5, "cni", version, false, true, false, ""},
		{"podman 5 on netavark", 5, "netavark", version, false, false, true, ""},
		{"podman 6", 6, "netavark", version, false, false, false, "Podman 6 has no CNI"},
		{"no --no-start", 5, "cni", "netavark-migrate 2 host json\n", false, false, false, "needs netavark-migrate"},
		{"a switch running", 5, "cni", version, true, false, false, ""},
	}
	for _, c := range cases {
		podmanMajor = func(context.Context) int { return c.major }
		podmanBackend = func(context.Context) string { return c.backend }
		version = c.version
		nvSwitch.mu.Lock()
		nvSwitch.running = c.running
		nvSwitch.mu.Unlock()
		st := s.netSwitchState(context.Background())
		if st.CanSwitch != c.canSwitch || st.CanBack != c.back || !strings.Contains(st.Note, c.note) {
			t.Errorf("%s: switch %v back %v note %q", c.name, st.CanSwitch, st.CanBack, st.Note)
		}
	}
	nvSwitch.mu.Lock()
	nvSwitch.running = false
	nvSwitch.mu.Unlock()
}

// netavark-migrate restarts podman_service, which keeps the tool's output
// open after the tool exits: the switch reads the report and moves on.
func TestMigrateSwitchDoesNotWaitForADaemon(t *testing.T) {
	tool := filepath.Join(t.TempDir(), "netavark-migrate")
	script := "#!/bin/sh\n" +
		"echo '{\"version\":2,\"action\":\"switch\",\"ok\":true,\"changed\":true,\"backend\":{\"changed\":true,\"from\":\"cni\",\"to\":\"netavark\"}}'\n" +
		"echo restarting podman_service >&2\n" +
		"sleep 30 &\n" +
		"exit 0\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldTool, oldDelay := migrateTool, migrateWaitDelay
	t.Cleanup(func() { migrateTool, migrateWaitDelay = oldTool, oldDelay })
	migrateTool, migrateWaitDelay = tool, 500*time.Millisecond
	var log strings.Builder
	start := time.Now()
	rep, code, err := migrateSwitch(context.Background(), "switch", &log)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("waited %v for a daemon's output", took)
	}
	if err != nil || code != 0 || rep == nil || rep.Backend.To != "netavark" {
		t.Fatalf("report %+v, code %d, err %v", rep, code, err)
	}
	if !strings.Contains(log.String(), "restarting podman_service") {
		t.Errorf("the tool's own log did not reach the output: %q", log.String())
	}
}
