package appjail

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
)

// healthWindow is how long updated jails are watched, as podman watches its
// containers after an update.
var healthWindow = 30 * time.Second

// What a watch asks of a jail; swapped by tests.
var (
	jailJID = func(ctx context.Context, jail string) string {
		out, err := exec.CommandContext(ctx, "jls", "-j", jail, "jid").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	jailProcs = func(ctx context.Context, jail string) int {
		out, err := exec.CommandContext(ctx, "ps", "-o", "pid=", "-J", jail).Output()
		if err != nil {
			return 0
		}
		return len(strings.Fields(string(out)))
	}
	jailHealth = serviceHealth
)

// watchJails says whether updated jails stay up.
//
// A jail is not a container: appjail keeps it up after its app dies (persist,
// which its own start needs), with nothing running inside, and `appjail
// status` still says running (netlab, 2026-10-17). So up alone proves
// nothing. Each jail fails if it goes down or is started again (its jid
// changes), or if nothing runs inside it any more; and at the end, if its app
// keeps crashing (s6 in its log, nothing on its port). A failure is an
// [error] line, which the page reads as a failed update.
func watchJails(ctx context.Context, w io.Writer, jails []svcJail, window time.Duration, after string) error {
	type watched struct {
		sj     svcJail
		jid    string
		failed bool
	}
	var ws []*watched
	for _, sj := range jails {
		if jid := jailJID(ctx, sj.jail); jid != "" {
			ws = append(ws, &watched{sj: sj, jid: jid})
		}
	}
	if len(ws) == 0 {
		return nil
	}
	names := make([]string, len(ws))
	for i, x := range ws {
		names[i] = x.sj.svc.Name
	}
	fmt.Fprintf(w, "\n%s%s stay%s up for %s\n", engine.WatchStartPrefix, strings.Join(names, ", "),
		map[bool]string{true: "s", false: ""}[len(ws) == 1], window)
	// Its own context, so ending the watch early cancels nothing else.
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	engine.WatchStarted(ctx, stop)
	ended := func() bool {
		if wctx.Err() == nil || ctx.Err() != nil {
			return false
		}
		fmt.Fprintln(w, engine.WatchEndedLine)
		return true
	}

	deadline := time.Now().Add(window)
	for {
		live := 0
		for _, x := range ws {
			if x.failed {
				continue
			}
			switch jid := jailJID(wctx, x.sj.jail); {
			case wctx.Err() != nil:
			case jid == "":
				fmt.Fprintf(w, "[error] %s's jail went down after %s -- see its logs\n", x.sj.svc.Name, after)
				x.failed = true
			case jid != x.jid:
				fmt.Fprintf(w, "[error] %s's jail started again since %s -- it is crash-looping; see its logs\n", x.sj.svc.Name, after)
				x.failed = true
			case jailProcs(wctx, x.sj.jail) == 0 && wctx.Err() == nil:
				fmt.Fprintf(w, "[error] %s stopped after %s: its jail is up with nothing running inside -- see its logs\n", x.sj.svc.Name, after)
				x.failed = true
			default:
				live++
			}
		}
		if ended() {
			return nil
		}
		if live == 0 || !time.Now().Before(deadline) {
			break
		}
		select {
		case <-wctx.Done():
			ended()
			return nil
		case <-time.After(2 * time.Second):
		}
	}
	// Running is not serving: s6 restarts a faulting service inside a jail
	// that stays up. Ask the app at the end.
	for _, x := range ws {
		if x.failed {
			continue
		}
		state, detail := jailHealth(wctx, x.sj.jail, x.sj.svc)
		if ended() {
			return nil
		}
		if state == "crashed" {
			if why, ok := strings.CutPrefix(detail, "the app keeps crashing: "); ok {
				fmt.Fprintf(w, "[error] %s keeps crashing since %s: %s\n", x.sj.svc.Name, after, why)
			} else {
				fmt.Fprintf(w, "[error] %s keeps crashing inside its jail since %s -- see its logs\n", x.sj.svc.Name, after)
			}
			x.failed = true
		}
	}
	var failed []string
	for _, x := range ws {
		if x.failed {
			failed = append(failed, x.sj.svc.Name)
		} else {
			fmt.Fprintf(w, "[fjord] %s has stayed up for %s\n", x.sj.svc.Name, window)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("did not stay up: %s", strings.Join(failed, ", "))
	}
	return nil
}
