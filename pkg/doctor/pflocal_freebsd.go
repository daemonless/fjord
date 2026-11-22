package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// sysctlConf is where a sysctl is kept for the next boot; tests replace it.
var sysctlConf = "/etc/sysctl.conf"

// pf redirects a published port only for packets it filters, and packets
// the host sends to itself skip it unless net.pf.filter_local is on. With it
// off a published port answers from every other machine and hangs from the
// host itself, on 127.0.0.1 and on its own address alike, whatever podman's
// network backend.

// pfLocalCommands are what installPfLocal runs, for the setup page.
var pfLocalCommands = []string{
	"sysctl net.pf.filter_local=1",
	"echo 'net.pf.filter_local=1' >> " + sysctlConf,
	"# at boot the setting only takes if pf is loaded before it is applied:",
	"sysrc -f /boot/loader.conf pf_load=YES",
	"# pf picks the setting up when it is enabled:",
	"pfctl -d && pfctl -e -f /etc/pf.conf",
}

// pfLocalCheck is optional and marked OnHost: a host reached only from other
// machines works without it, so it never counts as a problem there. A
// browser on the host itself is told, since its Open links go to localhost.
func pfLocalCheck() Check {
	return Check{
		ID: "pf-local", Name: "published ports answer from this host itself", Engine: "podman", HostOnly: true,
		Group:    "firewall",
		Optional: true,
		OnHost:   true,
		Probe:    pfLocalProbe,
		Install:  installPfLocal,
		Commands: pfLocalCommands,
		Action:   "Let pf redirect this host's own connections",
		Why:      "A published port is a pf redirect, and pf leaves the connections this host makes to itself alone unless net.pf.filter_local is on. With it off the port answers from every other machine but hangs from this host, on localhost and on its own address: an app's Open link does not load in a browser on this host, and neither does a reverse proxy or a curl on it. Turning it on makes pf filter the host's own traffic too, so rules in /etc/pf.conf then apply to it.",
		Fix:      strings.Join(pfLocalCommands, "\n"),
	}
}

// pfSkipsLoDetail is what the check says where pf.conf has `set skip on lo0`:
// the host's own connections to a published port pass lo0, and pf then
// never sees them whatever net.pf.filter_local says.
const pfSkipsLoDetail = "pf is told to skip lo0 (`set skip on lo0` in /etc/pf.conf), so it never redirects this host's own connections -- a published port answers from other machines but not from this host itself. That line is yours: take it out if your rules allow it, or open apps from another machine"

// pfLocalProbe asks pf itself: the setting can read 1 with nothing behind
// it, since pf only hooks the host's own traffic when it is enabled.
func pfLocalProbe(ctx context.Context) (Status, string) {
	v := strings.TrimSpace(run(ctx, "sysctl", "-n", "net.pf.filter_local"))
	return pfLocalStatus(v, pfLocalHooked(run(ctx, "pfilctl", "heads")), pfSkipsLo(ctx))
}

func pfLocalStatus(v string, hooked, skipsLo bool) (Status, string) {
	switch {
	case v != "0" && v != "1":
		return Unknown, "pf is not loaded"
	case skipsLo:
		return Warn, pfSkipsLoDetail
	case v == "1" && hooked:
		return OK, "on"
	case v == "1":
		return Warn, "set, but pf has not taken it up -- a published port answers from other machines but not from this host itself"
	}
	return Warn, "off -- a published port answers from other machines but not from this host itself"
}

// pfSkipsLo reports whether the loaded ruleset skips lo0.
func pfSkipsLo(ctx context.Context) bool {
	return pfSkips(run(ctx, "pfctl", "-s", "Interfaces", "-vv", "-i", "lo0"), "lo0")
}

// pfSkips reads `pfctl -s Interfaces -vv`, which marks a skipped interface
// "lo0 (skip)".
func pfSkips(out, nic string) bool {
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == nic && f[1] == "(skip)" {
			return true
		}
	}
	return false
}

// pfLocalHooked reports whether `pfilctl heads` lists a pf hook under
// inet-local, the point the host's own IPv4 connections pass.
func pfLocalHooked(heads string) bool {
	in := false
	for _, l := range strings.Split(heads, "\n") {
		f := strings.Fields(l)
		switch {
		case len(f) == 0:
		case len(f) >= 2 && (strings.HasPrefix(f[1], "IPv") || f[1] == "Ethernet"):
			in = f[0] == "inet-local"
		case in && strings.HasPrefix(f[len(f)-1], "pf:"):
			return true
		}
	}
	return false
}

// pfLocalOn is installPfLocal for a host where pf does not yet redirect its
// own connections, and nothing where it does.
func pfLocalOn(ctx context.Context, w io.Writer) error {
	if st, _ := pfLocalProbe(ctx); st == OK {
		return nil
	}
	if pfSkipsLo(ctx) {
		fmt.Fprintln(w, "# /etc/pf.conf has `set skip on lo0`: published ports stay unanswered from this host itself")
		return nil
	}
	return installPfLocal(ctx, w)
}

// installPfLocal turns the setting on now and for the next boot, then has
// pf take it up: it reads the setting when it is enabled. pf loaded by rc
// comes after sysctl.conf is applied, so the loader loads it instead. It
// changes nothing where pf skips lo0, which only the operator can undo.
func installPfLocal(ctx context.Context, w io.Writer) error {
	if pfSkipsLo(ctx) {
		return errors.New(pfSkipsLoDetail)
	}
	if err := runShown(ctx, w, nil, "sysctl", "net.pf.filter_local=1"); err != nil {
		return err
	}
	if err := keepSysctl(w, sysctlConf, "net.pf.filter_local", "1"); err != nil {
		return err
	}
	if err := runShown(ctx, w, nil, "sysrc", "-f", "/boot/loader.conf", "pf_load=YES"); err != nil {
		return err
	}
	if err := runShown(ctx, w, nil, "pfctl", "-d"); err != nil {
		return err
	}
	return runShown(ctx, w, nil, "pfctl", "-e", "-f", "/etc/pf.conf")
}

// keepSysctl sets name=value in a sysctl.conf, replacing a line that sets it
// to something else and leaving the rest of the file as it is.
func keepSysctl(w io.Writer, path, name, value string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	want := name + "=" + value
	var out []string
	found := false
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		k, _, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok && strings.TrimSpace(k) == name {
			if found {
				continue
			}
			found = true
			l = want
		}
		out = append(out, l)
	}
	if !found {
		if len(out) == 1 && out[0] == "" {
			out = nil
		}
		out = append(out, want)
	}
	fmt.Fprintf(w, "$ echo '%s' >> %s\n", want, path)
	return os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}
