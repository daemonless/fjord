package podman

import (
	"context"
	"os/exec"
	"strconv"
	"strings"

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
// own state stands. The three hooks are variables so tests can stand in.
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
	// logTail is the last few lines of the container's log.
	logTail = func(ctx context.Context, containerID string) string {
		out, _ := exec.CommandContext(ctx, "podman", "logs", "--tail", "12", containerID).CombinedOutput()
		return string(out)
	}
)

// serviceHealth judges a RUNNING container: "running" when a published port
// is listening (or the container publishes none and its log shows no fault),
// "crashed" when s6 keeps restarting the service, else "starting" (the app
// has not bound its port yet). The port is checked first: a healthy but quiet
// service logs nothing new, so its tail can still show the crash it has
// since recovered from.
func serviceHealth(ctx context.Context, c libpodContainer) (state, detail string) {
	jid := jailID(ctx, c.ID)
	if jid == "" {
		return "running", ""
	}
	probed := false
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
	if engine.CrashedInLog(logTail(ctx, c.ID)) {
		return "crashed", "the app inside the container keeps crashing -- see Logs"
	}
	if probed {
		return "starting", "container is up but nothing is listening on its port yet"
	}
	return "running", ""
}
