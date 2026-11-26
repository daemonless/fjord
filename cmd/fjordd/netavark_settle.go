package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"runtime"
	"strings"

	"github.com/daemonless/fjord/pkg/doctor"
	"github.com/daemonless/fjord/pkg/engine/podman"
)

// netavarkNeeds are the System checks a FreeBSD host has to pass before a
// container on podman's netavark backend has networking and names.
var netavarkNeeds = []string{"pf", "dns-port"}

// settleNetavark is podman.SettleHost: on a FreeBSD host whose podman uses
// netavark it runs the fix of each check in netavarkNeeds that is not
// satisfied, the same fix the System page's Set up button runs, so a host
// that moved from the CNI backend (a podman upgrade does that) needs nobody
// to click anything. It reports whether every one of them is satisfied now.
func (s *server) settleNetavark(ctx context.Context) bool {
	if runtime.GOOS != "freebsd" {
		return true
	}
	cfg := doctor.Config{Engines: s.engineNames(), FjordRoot: s.fjordRoot}
	ready, changed := true, false
	// The System page shows the checks as they were last run: run them again
	// once a fix went in, so it does not keep asking for what is done.
	defer func() {
		if changed {
			s.triggerConditions()
		}
	}()
	// netavark-migrate first, when the port has it: the host side as the
	// port does it. What it leaves undone, fjord's own fixes below still try.
	if migrateUsable(ctx) {
		rep, err := migrateHost(ctx)
		switch {
		case err != nil:
			log.Printf("netavark: netavark-migrate host: %v", err)
		default:
			for _, line := range rep.did() {
				changed = true
				s.noteHost(netavarkNotice, netavarkNoticeTitle, line)
			}
			if rep.ExitCode != 0 || len(rep.Warnings) > 0 {
				log.Printf("netavark: netavark-migrate host (exit %d): %s", rep.ExitCode, rep.problems())
			} else if rep.Changed {
				log.Printf("netavark: netavark-migrate set the host up: %s", strings.Join(rep.did(), " "))
			}
			// It sets the port; containers already using names follow it
			// only once restarted, which fjord's own fix does (it writes
			// nothing when the port is set).
			if rep.DNS.Changed {
				var out bytes.Buffer
				if err := doctor.Install(doctor.Unattended(ctx), cfg, "dns-port", &out); err != nil {
					log.Printf("netavark: restart podman's DNS on its new port: %v", err)
				}
			}
		}
	}
	for _, id := range netavarkNeeds {
		r, ok := doctor.RunOne(ctx, cfg, id)
		if !ok || r.Status == doctor.OK {
			continue
		}
		if r.Status == doctor.Unknown {
			// Cannot be read from here, so it cannot be judged or fixed.
			continue
		}
		// Whether the file already had the lines decides what the note may
		// claim: the fix then only loads them and keeps no copy.
		hadPf := id == "pf" && pfConfHasNetavark()
		var out bytes.Buffer
		if err := doctor.Install(doctor.Unattended(ctx), cfg, id, &out); err != nil {
			ready = false
			log.Printf("netavark: could not set up %q by itself (%s): %v", id, r.Detail, err)
			continue
		}
		changed = true
		after, _ := doctor.RunOne(ctx, cfg, id)
		if after.Status != doctor.OK && after.Status != doctor.Unknown {
			ready = false
			log.Printf("netavark: set up %q but it still reports: %s", id, after.Detail)
			continue
		}
		log.Printf("netavark: set up %q by itself (it reported: %s)\n%s", id, r.Detail, strings.TrimRight(out.String(), "\n"))
		did := netavarkDid[id]
		if hadPf {
			did = "Loaded netavark's anchors into pf; /etc/pf.conf already had them."
		}
		s.noteHost(netavarkNotice, netavarkNoticeTitle, did)
	}
	return ready
}

// One notice for everything fjord did for the move to netavark.
const (
	netavarkNotice      = "netavark"
	netavarkNoticeTitle = "podman on this host now uses the netavark network backend, and fjord set the host up for it"
)

// netavarkDid says, per fix, what was changed and how to see or undo it.
var netavarkDid = map[string]string{
	"pf":       "Added netavark's anchors to /etc/pf.conf and loaded them. The file as it was is kept as /etc/pf.conf.before-fjord.",
	"dns-port": "Port 53 is taken on this host, so podman's DNS server now listens on another port (set in /usr/local/etc/containers/containers.conf.d/50-fjord-dns-port.conf).",
}

// noteCarriedOver records the networks fjord made again under netavark.
func (s *server) noteCarriedOver(name string) {
	s.noteHost(netavarkNotice, netavarkNoticeTitle, "Made the network "+name+" again, with the name and addresses it had, so its stacks start as they were. Its old definition is kept in "+podman.KeptConflistDir()+".")
}

// useNetavarkSettle hands settleNetavark to the podman engine.
func useNetavarkSettle(s *server) {
	podman.SettleHost = s.settleNetavark
	podman.CarriedOver = s.noteCarriedOver
}

// pfConfHasNetavark reports whether /etc/pf.conf already names netavark's
// anchor.
func pfConfHasNetavark() bool {
	b, err := os.ReadFile("/etc/pf.conf")
	return err == nil && strings.Contains(string(b), `anchor "netavark/*"`)
}
