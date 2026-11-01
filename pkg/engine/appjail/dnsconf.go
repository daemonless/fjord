package appjail

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
)

// Jail names, answered by dnsmasq from the hosts file appjail-dns keeps.
//
// appjail's own config (StockDNSConf) answers on every interface -- an open
// resolver on the LAN -- and forwards only to OpenDNS, so jails lose the
// LAN's names. fjord writes its own instead: the same hosts file, answered
// only on the AppJail networks' bridges, everything else forwarded to this
// host's resolvers (dnsmasq reads /etc/resolv.conf when told nothing else).
const (
	DNSConf      = "/usr/local/etc/fjord/dnsmasq-appjail.conf"
	StockDNSConf = "/usr/local/share/appjail/files/dnsmasq.conf"
)

// dnsConfBody is the config for these AppJail network bridges. With none yet
// it listens on loopback only: bind-dynamic with no interface would answer
// everywhere.
func dnsConfBody(bridges []string) string {
	var b strings.Builder
	b.WriteString("# Written by fjord: AppJail jail names, for jails. Rewritten when fjord\n")
	b.WriteString("# adds or removes an AppJail network, so edits here do not last.\n")
	b.WriteString("addn-hosts=/var/tmp/appjail-hosts\n")
	// .appjail names are the jails' own: answered here, never asked of the
	// LAN's resolver, and neither is a bare name with no dot in it.
	b.WriteString("local=/appjail/\n")
	b.WriteString("domain-needed\n")
	b.WriteString("# Only on the AppJail networks, never on the LAN.\n")
	b.WriteString("bind-dynamic\n")
	if len(bridges) == 0 {
		b.WriteString("listen-address=127.0.0.1\n")
	}
	for _, br := range bridges {
		fmt.Fprintf(&b, "interface=%s\n", br)
	}
	return b.String()
}

// DNSConfBody is the config for the AppJail networks there are now; appjail
// names each network's bridge after it.
func DNSConfBody(ctx context.Context) string {
	var bridges []string
	for _, n := range listVirtualnets(ctx) {
		bridges = append(bridges, n.Name)
	}
	sort.Strings(bridges)
	return dnsConfBody(bridges)
}

// WriteDNSConf writes fjord's config for the networks there are now and says
// whether it changed.
func WriteDNSConf(ctx context.Context) (bool, error) {
	body := DNSConfBody(ctx)
	if old, err := os.ReadFile(DNSConf); err == nil && string(old) == body {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(DNSConf), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(DNSConf, []byte(body), 0o644)
}

// DNSConfInUse says dnsmasq is set to run with fjord's config.
func DNSConfInUse(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "sysrc", "-n", "dnsmasq_conf").Output()
	return err == nil && strings.TrimSpace(string(out)) == DNSConf
}

// refreshDNS follows a network added or removed: when fjord's config is the
// one in use, it is rewritten and dnsmasq restarted, so a jail on the new
// network finds the others. A dnsmasq fjord did not set up is left alone.
func refreshDNS(ctx context.Context) {
	if !DNSConfInUse(ctx) {
		return
	}
	if changed, err := WriteDNSConf(ctx); err == nil && changed {
		restartDNS(ctx)
	}
}

// restartDNS restarts dnsmasq, then appjail-dns. appjail-dns reloads dnsmasq
// when the names change and exits when that fails, which it does while
// dnsmasq is restarting ("dnsmasq not running?", netlab 2026-10-17): the
// names then stopped being written at all.
func restartDNS(ctx context.Context) {
	_ = exec.CommandContext(ctx, "service", "dnsmasq", "restart").Run()
	_ = exec.CommandContext(ctx, "service", "appjail-dns", "restart").Run()
}

// Virtualnets are appjail's own networks (name, subnet, gateway).
func Virtualnets(ctx context.Context) []engine.Network { return listVirtualnets(ctx) }

// followNewBridges restarts dnsmasq when an AppJail network's bridge came up
// after it started. appjail makes a network's bridge when its first jail
// joins, and dnsmasq on FreeBSD binds only to the interfaces there at start
// (bind-dynamic falls back to bind-interfaces): the first jails on a new
// network could not resolve anything until something restarted it.
func followNewBridges(ctx context.Context) {
	if !DNSConfInUse(ctx) {
		return
	}
	out, err := exec.CommandContext(ctx, "sockstat", "-4l", "-p", "53").Output()
	if err != nil {
		return
	}
	for _, n := range listVirtualnets(ctx) {
		if n.Gateway != "" && interfaceExists(ctx, n.Name) && !listensOn(string(out), n.Gateway) {
			restartDNS(ctx)
			return
		}
	}
}

// listensOn says sockstat output has a dnsmasq socket on addr:53.
func listensOn(sockstat, addr string) bool {
	for _, l := range strings.Split(sockstat, "\n") {
		f := strings.Fields(l)
		if len(f) >= 6 && f[1] == "dnsmasq" && f[5] == addr+":53" {
			return true
		}
	}
	return false
}
