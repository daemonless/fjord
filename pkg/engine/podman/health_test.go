package podman

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
)

// A container podman reports as running, with one published port.
func upContainer(name string) libpodContainer {
	c := libpodContainer{ID: name + "-id", Names: []string{"/" + name}, State: "running",
		Labels: map[string]string{"io.podman.compose.service": name}}
	c.Ports = append(c.Ports, libpodPort{HostPort: 3456, ContainerPort: 3456, Protocol: "tcp"})
	return c
}

func stubProbes(t *testing.T, listens bool, log string) {
	t.Helper()
	oldJ, oldL, oldT := jailID, listening, logTail
	jailID = func(context.Context, string) string { return "189" }
	listening = func(context.Context, string, string, int) bool { return listens }
	logTail = func(context.Context, string, time.Time) string { return log }
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

func TestUnhealthyStatusAndContainerRestartLoop(t *testing.T) {
	stubProbes(t, false, "")
	jailID = func(context.Context, string) string { return "" }

	unhealthy := upContainer("app")
	unhealthy.Status = "unhealthy"
	if st, detail := serviceHealth(context.Background(), unhealthy); st != "crashed" || detail == "" {
		t.Fatalf("unhealthy container: got %q %q, want crashed", st, detail)
	}

	looping := upContainer("worker")
	looping.Ports = nil
	looping.Restarts = 4
	looping.StartedAt = time.Now().Add(-10 * time.Second).Unix()
	agg := aggregateStatus([]libpodContainer{looping}, func(c libpodContainer) (string, string) {
		return serviceHealth(context.Background(), c)
	})
	if agg.Containers[0].State != "crashed" || agg.Containers[0].Restarts != 4 {
		t.Fatalf("looping container: got %+v, want crashed with Restarts=4", agg.Containers[0])
	}

	exited := libpodContainer{
		ID: "db-id", Names: []string{"/db"}, State: "exited",
		Exited: true, ExitCode: 137, Restarts: 2,
		Labels: map[string]string{"io.podman.compose.service": "db"},
	}
	aggExited := aggregateStatus([]libpodContainer{exited}, nil)
	if aggExited.Containers[0].ExitCode != 137 || aggExited.Containers[0].Detail == "" {
		t.Fatalf("exited container: got %+v, want ExitCode=137 and Detail", aggExited.Containers[0])
	}
}

type rewriteHost struct {
	target string
	rt     http.RoundTripper
}

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Host = r.target
	return r.rt.RoundTrip(clone)
}

func TestLogTailBoundsRestartScanAndTimeout(t *testing.T) {
	var gotSince string
	var slow bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSince = r.URL.Query().Get("since")
		if slow {
			time.Sleep(200 * time.Millisecond)
		}
		w.Write([]byte("error: permission denied\n"))
	}))
	defer srv.Close()

	oldJ, oldL, oldC, oldTimeout := jailID, listening, logClient, logTailTimeout
	jailID = func(context.Context, string) string { return "" }
	client := &http.Client{Transport: rewriteHost{target: strings.TrimPrefix(srv.URL, "http://"), rt: http.DefaultTransport}}
	logClient = func() *http.Client { return client }
	logTailTimeout = 40 * time.Millisecond
	t.Cleanup(func() {
		jailID, listening, logClient, logTailTimeout = oldJ, oldL, oldC, oldTimeout
	})

	started := time.Now().Add(-10 * time.Second).Truncate(time.Second)
	looping := upContainer("vpn")
	looping.Ports = nil
	looping.Restarts = 1400
	looping.StartedAt = started.Unix()

	st, detail := serviceHealth(context.Background(), looping)
	wantSince := strconv.FormatInt(started.Add(-logTailWindow).Unix(), 10)
	if gotSince != wantSince {
		t.Fatalf("since = %q, want %q", gotSince, wantSince)
	}
	if st != "crashed" || !strings.Contains(detail, "permission denied") {
		t.Fatalf("got %q %q, want crashed with log reason", st, detail)
	}

	// An s6 check inside a single long-lived container omits since so an older
	// fatal line in a quiet container is still read.
	jailID = func(context.Context, string) string { return "42" }
	listening = func(context.Context, string, string, int) bool { return false }
	gotSince = "unset"
	quiet := upContainer("app")
	quiet.StartedAt = time.Now().Add(-2 * time.Hour).Unix()
	_, _ = serviceHealth(context.Background(), quiet)
	if gotSince != "" {
		t.Fatalf("s6 check sent since=%q, want empty", gotSince)
	}

	// A stalled log driver yields within logTailTimeout and still reports the
	// restart loop without its log line.
	jailID = func(context.Context, string) string { return "" }
	slow = true
	t0 := time.Now()
	st, detail = serviceHealth(context.Background(), looping)
	if elapsed := time.Since(t0); elapsed > 150*time.Millisecond {
		t.Fatalf("stalled logTail took %v, want bounded by timeout", elapsed)
	}
	if st != "crashed" || !strings.Contains(detail, "restarted 1400 times") {
		t.Fatalf("after timeout: got %q %q, want crashed fallback", st, detail)
	}
}
