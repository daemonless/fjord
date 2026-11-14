package podman

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
)

// A container being up says nothing about the app inside it: under s6 a
// service that dies on its config is restarted every five seconds behind a
// container podman reports as Up, with RestartCount never moving. Vikunja
// given a public URL without a scheme read as "running" for as long as
// anyone cared to look. So, as the appjail engine does for its jails, ask the
// app itself: a listening published port is proof it serves; s6 reporting a
// fault in the log tail is proof it does not.
//
// On FreeBSD every podman container is a jail named by its full id, so the
// listener probe is sockstat -j without entering the container. Where that
// is not so (no jail by that name), nothing is probed and the container's
// own state stands. The hooks are variables so tests can stand in.
var (
	// jailID returns the jail id a container runs in, or "" when it is not a jail.
	jailID = func(ctx context.Context, containerID string) string {
		out, err := exec.CommandContext(ctx, "jls", "-j", containerID, "jid").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	// listening reports whether something in the jail listens on port/proto.
	listening = func(ctx context.Context, jid, proto string, port int) bool {
		out, err := exec.CommandContext(ctx, "sockstat", "-46", "-l", "-j", jid, "-P", proto, "-p", strconv.Itoa(port)).Output()
		// header + at least one socket
		return err == nil && strings.Count(strings.TrimSpace(string(out)), "\n") >= 1
	}
	// logTail is the last few lines of the container's log, from the API: the
	// podman CLI it used to start took 0.3 s a container, and every status poll
	// of a stack whose app publishes no host port read it -- 14 at once took
	// 2.6 s on a busy host, past the page's wait, so every stack read "busy".
	logTail = func(ctx context.Context, containerID string, since time.Time) string {
		ctx, cancel := context.WithTimeout(ctx, logTailTimeout)
		defer cancel()
		u := "http://d/v4.0.0/libpod/containers/" + url.PathEscape(containerID) + "/logs?stdout=true&stderr=true&tail=12"
		if !since.IsZero() {
			u += "&since=" + strconv.FormatInt(since.Unix(), 10)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return ""
		}
		resp, err := logClient().Do(req)
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return ""
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return demuxLogs(b)
	}
	logClient = sync.OnceValue(newSocketClient)
)

// logTailWindow is how far before a crash-looping container's last start its
// log is read: enough for the previous crash's last words, without asking
// podman for the whole log of a container restarted a thousand times.
const logTailWindow = 5 * time.Minute

// logTailTimeout bounds one log read, so a slow log driver cannot hold up a
// status poll. A variable so tests can shorten it.
var logTailTimeout = time.Second

// serviceHealth judges a RUNNING container: "running" when a published port
// is listening (or the container publishes none and its log shows no fault),
// "crashed" when s6 keeps restarting the service, else "starting" (the app
// has not bound its port yet). The port is checked first: a healthy but quiet
// service logs nothing new, so its tail can still show the crash it has
// since recovered from.
func serviceHealth(ctx context.Context, c libpodContainer) (state, detail string) {
	return serviceHealthWithin(ctx, c, startGrace)
}

// startGrace is how long after its container starts an app's restarts read
// as starting, not crashed. On first boot an app restarts under s6 while its
// database initialises -- Vikunja on PostgreSQL, a few times in 20 s -- which
// in the log is exactly a crash loop, and the page said "crashed" for a stack
// that was coming up fine. Past this, the same signs are a crash.
var startGrace = 60 * time.Second

// serviceHealthWithin is serviceHealth with the grace given: the watches
// after an update or install judge with none, at the end of their window.
func serviceHealthWithin(ctx context.Context, c libpodContainer, grace time.Duration) (state, detail string) {
	if strings.EqualFold(c.Status, "unhealthy") {
		if why := engine.CrashReason(logTail(ctx, c.ID, time.Time{})); why != "" {
			return "crashed", "container healthcheck reports unhealthy: " + why
		}
		return "crashed", "container healthcheck reports unhealthy -- see Logs"
	}
	jid := jailID(ctx, c.ID)
	probed := false
	if jid != "" {
		for _, p := range c.Ports {
			if p.ContainerPort <= 0 {
				continue
			}
			proto := p.Protocol
			if proto == "" {
				proto = "tcp"
			}
			probed = true
			if listening(ctx, jid, proto, p.ContainerPort) {
				return "running", ""
			}
		}
	}
	// A container that podman has already restarted more than once and whose
	// current run is still inside the startup window is crash-looping at the
	// container level (restart: always / unless-stopped), even on Linux where
	// jid is empty or when the app inside does not run under s6.
	if c.Restarts >= 2 && (grace == 0 || (c.StartedAt > 0 && time.Since(time.Unix(c.StartedAt, 0)) < grace)) {
		since := time.Now().Add(-logTailWindow)
		if c.StartedAt > 0 {
			since = time.Unix(c.StartedAt, 0).Add(-logTailWindow)
		}
		if why := engine.CrashReason(logTail(ctx, c.ID, since)); why != "" {
			return "crashed", fmt.Sprintf("crash-looping (restarted %d times): %s", c.Restarts, why)
		}
		return "crashed", fmt.Sprintf("crash-looping: restarted %d times -- see Logs", c.Restarts)
	}
	if jid == "" {
		return "running", ""
	}
	// A log read that timed out is empty and reads as no crash: a slow log
	// costs the crash verdict, never the page's wait.
	if tail := logTail(ctx, c.ID, time.Time{}); engine.CrashedInLog(tail) {
		if c.StartedAt > 0 && time.Since(time.Unix(c.StartedAt, 0)) < grace {
			return "starting", "starting up -- the app restarted while it waits (for its database, say)"
		}
		if why := engine.CrashReason(tail); why != "" {
			return "crashed", "the app keeps crashing: " + why
		}
		return "crashed", "the app inside the container keeps crashing -- see Logs"
	}
	if probed {
		return "starting", "container is up but nothing is listening on its port yet"
	}
	return "running", ""
}

// demuxLogs is the text of a logs response: a container without a terminal
// sends its output and errors as frames, an 8-byte header (stream, 0, 0, 0,
// big-endian length) before each; one with a terminal sends plain text.
func demuxLogs(b []byte) string {
	var out []byte
	framed := false
	for len(b) >= 8 && b[0] <= 2 && b[1] == 0 && b[2] == 0 && b[3] == 0 {
		framed = true
		n := int(binary.BigEndian.Uint32(b[4:8]))
		if n > len(b)-8 {
			n = len(b) - 8
		}
		out = append(out, b[8:8+n]...)
		b = b[8+n:]
	}
	if !framed {
		return string(b)
	}
	return string(out)
}
