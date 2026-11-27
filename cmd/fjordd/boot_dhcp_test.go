package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

// dhcpEngine gets no DHCP answer for its first fails bring-ups, then comes up.
type dhcpEngine struct {
	restartEngine
	fails int
	ups   *int
	up    *bool
}

const noDHCPLine = "[error] no DHCP server answered on network lan (bridge lanbridge), so the service got no address and did not start.\n"

func (e dhcpEngine) Up(ctx context.Context, s *stack.Stack) (io.ReadCloser, error) {
	*e.ups++
	if *e.ups <= e.fails {
		return io.NopCloser(strings.NewReader(noDHCPLine)), nil
	}
	*e.up = true
	return io.NopCloser(strings.NewReader("started\n")), nil
}

func (e dhcpEngine) Status(ctx context.Context, s *stack.Stack) (engine.StackStatus, error) {
	if *e.up {
		return engine.StackStatus{State: "running"}, nil
	}
	return engine.StackStatus{State: "stopped"}, nil
}

func bootDHCPFixture(t *testing.T, fails int) (*server, *int) {
	t.Helper()
	s := restartFixture(t, onExit)
	ups, up := 0, false
	s.backends["podman"] = dhcpEngine{restartEngine: restartEngine{Backend: engine.Unavailable("podman"), caps: onExit}, fails: fails, ups: &ups, up: &up}
	if err := s.manager.SetDesiredState("gitea", "running"); err != nil {
		t.Fatal(err)
	}
	old, oldWaits := bootMarker, bootDHCPWaits
	bootMarker = filepath.Join(t.TempDir(), "fjordd.booted") // absent: a host boot
	bootDHCPWaits = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { bootMarker, bootDHCPWaits = old, oldWaits })
	return s, &ups
}

// At a host boot a stack whose network gives no DHCP answer yet is tried
// again, and the tries before it came up leave no failure behind.
func TestStartOnBootRetriesNoDHCP(t *testing.T) {
	s, ups := bootDHCPFixture(t, 2)
	startOnBoot(s)
	if *ups != 3 {
		t.Fatalf("brought up %d times, want 3", *ups)
	}
	if st, _ := s.manager.LoadState("gitea"); st == nil || st.LastFailure != nil {
		t.Fatalf("LastFailure = %+v, want none once it came up", st.LastFailure)
	}
}

// Still no answer after the last wait: the failure is recorded, and says it
// was tried for the whole boot window.
func TestStartOnBootGivesUpAfterTheWaits(t *testing.T) {
	s, ups := bootDHCPFixture(t, 100)
	startOnBoot(s)
	if *ups != 1+len(bootDHCPWaits) {
		t.Fatalf("brought up %d times, want %d", *ups, 1+len(bootDHCPWaits))
	}
	st, _ := s.manager.LoadState("gitea")
	if st == nil || st.LastFailure == nil || !strings.HasPrefix(st.LastFailure.Message, "Tried for 1 minute after the host started: no DHCP server answered") {
		t.Fatalf("LastFailure = %+v", st.LastFailure)
	}
}

// A fjordd restart is not a host boot: no DHCP answer then is recorded at
// once, as before.
func TestStartOnBootNoRetryOnRestart(t *testing.T) {
	s, ups := bootDHCPFixture(t, 100)
	if err := os.WriteFile(bootMarker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	startOnBoot(s)
	if *ups != 1 {
		t.Fatalf("brought up %d times, want 1", *ups)
	}
	if st, _ := s.manager.LoadState("gitea"); st == nil || st.LastFailure == nil {
		t.Fatal("no failure recorded")
	}
}
