package podman

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
)

// A container podman reports as running, with one published port.
func upContainer(name string) libpodContainer {
	c := libpodContainer{ID: name + "-id", Names: []string{"/" + name}, State: "running",
		Labels: map[string]string{"io.podman.compose.service": name}}
	c.Ports = append(c.Ports, struct {
		HostPort      int    `json:"host_port"`
		ContainerPort int    `json:"container_port"`
		Protocol      string `json:"protocol"`
	}{HostPort: 3456, ContainerPort: 3456, Protocol: "tcp"})
	return c
}

func stubProbes(t *testing.T, listens bool, log string) {
	t.Helper()
	oldJ, oldL, oldT := jailID, listening, logTail
	jailID = func(context.Context, string) string { return "189" }
	listening = func(context.Context, string, string, int) bool { return listens }
	logTail = func(context.Context, string) string { return log }
	t.Cleanup(func() { jailID, listening, logTail = oldJ, oldL, oldT })
}

const faultTail = `[INFO] Starting vikunja...
time=2026-10-01T19:33:58.424Z level=ERROR msg="service.publicurl must include http:// or https:// scheme, got: f/"
[s6] Service 'vikunja' crashed (Exit: 1, Signal: 0)
[s6] Waiting 5 seconds before restart...`

// Vikunja given "f/" as its public URL: the container stays Up while s6
// restarts the app every five seconds, and nothing ever listens.
func TestCrashLoopInsideUpContainerReadsAsCrashed(t *testing.T) {
	stubProbes(t, false, faultTail)
	st := aggregateStatus([]libpodContainer{upContainer("vikunja")}, func(c libpodContainer) (string, string) { return serviceHealth(context.Background(), c) })
	if st.Containers[0].State != "crashed" || st.Containers[0].Detail == "" {
		t.Fatalf("got %q %q, want crashed with a reason", st.Containers[0].State, st.Containers[0].Detail)
	}
	if st.State != "partial" {
		t.Errorf("stack state %q, want partial (up but not serving)", st.State)
	}
}

// The port listening settles it, whatever the log tail still says.
func TestListeningPortWinsOverOldCrashInLog(t *testing.T) {
	stubProbes(t, true, faultTail)
	st := aggregateStatus([]libpodContainer{upContainer("vikunja")}, func(c libpodContainer) (string, string) { return serviceHealth(context.Background(), c) })
	if st.Containers[0].State != "running" || st.State != "running" {
		t.Fatalf("got %q / %q, want running", st.Containers[0].State, st.State)
	}
}

// Nothing listening and no fault: it is still booting.
func TestNotListeningYetIsStarting(t *testing.T) {
	stubProbes(t, false, "[init] Starting s6 supervision...")
	st := aggregateStatus([]libpodContainer{upContainer("app")}, func(c libpodContainer) (string, string) { return serviceHealth(context.Background(), c) })
	if st.Containers[0].State != "starting" || st.State != "partial" {
		t.Fatalf("got %q / %q, want starting / partial", st.Containers[0].State, st.State)
	}
}

// No ports to probe and a clean log: the container's own word stands. And a
// host that is not running containers as jails is never probed at all.
func TestNoPortsAndNoJailStayRunning(t *testing.T) {
	stubProbes(t, false, "")
	c := upContainer("db")
	c.Ports = nil
	if state, _ := serviceHealth(context.Background(), c); state != "running" {
		t.Errorf("no ports, clean log: got %q, want running", state)
	}
	jailID = func(context.Context, string) string { return "" }
	if state, _ := serviceHealth(context.Background(), upContainer("linux")); state != "running" {
		t.Errorf("not a jail: got %q, want running", state)
	}
}

// Vikunja on PostgreSQL, first boot: it restarts under s6 while the database
// initialises -- in the log, a crash loop. Within startGrace of the container
// starting the page says starting; past it, and for the watches (no grace),
// the same log is a crash.
func TestRestartsWhileStartingAreNotCrashed(t *testing.T) {
	stubProbes(t, false, faultTail)
	c := upContainer("vikunja")
	c.StartedAt = time.Now().Add(-20 * time.Second).Unix()
	if st, detail := serviceHealth(context.Background(), c); st != "starting" || detail == "" {
		t.Fatalf("20 s after start: %q %q, want starting with a reason", st, detail)
	}
	if st, _ := serviceHealthWithin(context.Background(), c, 0); st != "crashed" {
		t.Fatalf("no grace (the watches): %q, want crashed", st)
	}
	c.StartedAt = time.Now().Add(-2 * time.Minute).Unix()
	if st, _ := serviceHealth(context.Background(), c); st != "crashed" {
		t.Fatalf("2 min after start: %q, want crashed", st)
	}
}

// The API's log stream for a container without a terminal is framed; the
// crash check must see the text, not the headers.
func TestDemuxLogs(t *testing.T) {
	frame := func(stream byte, text string) []byte {
		h := []byte{stream, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(h[4:], uint32(len(text)))
		return append(h, text...)
	}
	b := append(frame(1, "[INFO] Starting vikunja...\n"), frame(2, "[s6] Service 'vikunja' crashed (Exit: 1, Signal: 0)\n")...)
	got := demuxLogs(b)
	if got != "[INFO] Starting vikunja...\n[s6] Service 'vikunja' crashed (Exit: 1, Signal: 0)\n" {
		t.Fatalf("framed: %q", got)
	}
	if !engine.CrashedInLog(got) {
		t.Error("the crash line is not seen through the frames")
	}
	if got := demuxLogs([]byte("plain text from a tty\n")); got != "plain text from a tty\n" {
		t.Errorf("tty: %q", got)
	}
}
