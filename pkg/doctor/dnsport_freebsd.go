package doctor

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/engine/podman"
)

// Under netavark podman starts aardvark-dns on each network's gateway
// address, port 53. A DNS server bound to port 53 on every address takes
// those too: aardvark-dns cannot bind and every container on a network with
// name resolution fails to start. The dnsmasq fjord sets up for AppJail's
// jail names binds that way on purpose (appjail.DNSConf says why), so a host
// with both engines always hits it.
//
// The way out is podman's own setting: dns_bind_port moves aardvark-dns to
// another port and netavark redirects each gateway's port 53 to it in pf, so
// nothing changes inside a container.

// containersConfDirs are where podman reads containers.conf drop-ins on
// FreeBSD, and dnsPortConf the one fjord writes.
var (
	containersConf     = "/usr/local/etc/containers/containers.conf"
	containersConfDrop = "/usr/local/etc/containers/containers.conf.d"
)

const (
	dnsPortConfName = "50-fjord-dns-port.conf"
	// dnsPortFirst is the port fjord moves aardvark-dns to, or the next free
	// one after it.
	dnsPortFirst = 5300
)

// dnsPortCheck is the port 53 check for the netavark backend.
func dnsPortCheck() Check {
	// What the button will run depends on the host as it is now: the port it
	// would pick, and the containers it has to restart.
	plan := func() []string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return dnsPortCommands(dnsPortTarget(), dnsBindPort() == 0, dnsContainers(ctx))
	}
	return Check{
		ID: "dns-port", Name: "container name resolution can start (port 53)", Engine: "podman", HostOnly: true,
		Group:    "networking",
		Probe:    dnsPortProbe,
		Install:  installDNSPort,
		Commands: plan(),
		Action:   "Move podman's DNS server off port 53",
		Why:      "podman starts its DNS server (aardvark-dns) on port 53 of each network's own address. Another DNS server that holds port 53 on every address of this host, as the one for AppJail's jail names does, blocks it, and containers on a network with name resolution then fail to start. Moving podman's to another port fixes it: podman redirects port 53 to it, so nothing changes inside a container. Containers already using name resolution are restarted once, so they follow the move.",
		FixFunc:  func() string { return strings.Join(plan(), "\n") },
	}
}

// dnsContainers are the running containers a move of the port restarts;
// tests replace it.
var dnsContainers = podman.DNSContainers

// dnsPortTarget is the port aardvark-dns should end up on: the one already
// configured, else the first free one from dnsPortFirst.
func dnsPortTarget() int {
	if p := dnsBindPort(); p != 0 {
		return p
	}
	return freeDNSPort()
}

// dnsPortCommands are what installDNSPort runs, for the setup page: the
// drop-in when the port is not configured yet, then the restarts that make
// running containers follow it.
func dnsPortCommands(port int, write bool, restart []string) []string {
	var cmds []string
	if write {
		path := filepath.Join(containersConfDrop, dnsPortConfName)
		cmds = append(cmds,
			"mkdir -p "+containersConfDrop,
			"printf '[network]\\ndns_bind_port = "+strconv.Itoa(port)+"\\n' > "+path)
	}
	cmds = append(cmds, "service podman_service onerestart")
	if len(restart) > 0 {
		cmds = append(cmds,
			"# the containers that use name resolution restart once, to follow the move:",
			"pkill -x aardvark-dns",
			"podman restart "+strings.Join(restart, " "))
	}
	return cmds
}

// dnsPortProbe: fine when aardvark-dns is off port 53 and listens where
// podman is told to redirect, or nothing else holds port 53 on every address.
func dnsPortProbe(ctx context.Context) (Status, string) {
	listening := run(ctx, "sockstat", "-l", "-P", "udp")
	want := dnsBindPort()
	// A port set while containers ran: podman redirects port 53 to the new
	// one, the server they started still listens on the old one, and every
	// lookup between containers goes unanswered.
	if stale := staleAardvarkPort(listening, want); stale != 0 {
		to := want
		if to == 0 {
			to = 53
		}
		return Warn, fmt.Sprintf("podman's DNS server still listens on port %d while podman now uses port %d -- containers cannot find each other by name until the ones using it are restarted", stale, to)
	}
	if want != 0 && want != 53 {
		return OK, "podman's DNS server uses port " + strconv.Itoa(want)
	}
	holder := wildcardDNS(listening)
	if holder == "" {
		return OK, "port 53 is free for podman's DNS server"
	}
	return Warn, holder + " holds port 53 on every address of this host, so podman's DNS server cannot start -- containers on a network with name resolution will fail to start"
}

// staleAardvarkPort is the port a running aardvark-dns listens on when it is
// not the configured one (0 = podman's default, 53); 0 when none is running
// or it is where it should be. sockstat cuts the command name short.
func staleAardvarkPort(sockstat string, configured int) int {
	if configured == 0 {
		configured = 53
	}
	for _, l := range strings.Split(sockstat, "\n") {
		f := strings.Fields(l)
		if len(f) < 6 || !strings.HasPrefix(f[1], "aardvark") {
			continue
		}
		i := strings.LastIndexByte(f[5], ':')
		if i < 0 {
			continue
		}
		if p, err := strconv.Atoi(f[5][i+1:]); err == nil && p != configured {
			return p
		}
	}
	return 0
}

// wildcardDNS names the command bound to port 53 on the wildcard address in
// sockstat's listing, "" when none is. aardvark-dns itself binds one address
// per network, never the wildcard.
func wildcardDNS(sockstat string) string {
	for _, l := range strings.Split(sockstat, "\n") {
		f := strings.Fields(l)
		// USER COMMAND PID FD PROTO LOCAL FOREIGN
		if len(f) < 6 || f[0] == "USER" {
			continue
		}
		if f[5] == "*:53" {
			return f[1]
		}
	}
	return ""
}

// dnsBindPort is the dns_bind_port podman will use, 0 when it is not set:
// containers.conf, then its drop-ins in name order, the last one winning.
func dnsBindPort() int {
	files := []string{containersConf}
	drops, _ := filepath.Glob(filepath.Join(containersConfDrop, "*.conf"))
	sort.Strings(drops)
	port := 0
	for _, f := range append(files, drops...) {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if p := parseDNSBindPort(string(b)); p != 0 {
			port = p
		}
	}
	return port
}

// parseDNSBindPort reads dns_bind_port from the [network] table of one
// containers.conf, 0 when it is not set there.
func parseDNSBindPort(conf string) int {
	section, port := "", 0
	for _, l := range strings.Split(conf, "\n") {
		l = strings.TrimSpace(l)
		if i := strings.IndexByte(l, '#'); i >= 0 {
			l = strings.TrimSpace(l[:i])
		}
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			section = strings.TrimSpace(l[1 : len(l)-1])
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || section != "network" || strings.TrimSpace(k) != "dns_bind_port" {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 && n < 65536 {
			port = n
		}
	}
	return port
}

// freeDNSPort is the first port from dnsPortFirst nothing listens on.
func freeDNSPort() int {
	for p := dnsPortFirst; p < dnsPortFirst+50; p++ {
		l, err := net.ListenPacket("udp", ":"+strconv.Itoa(p))
		if err != nil {
			continue
		}
		l.Close()
		return p
	}
	return dnsPortFirst
}

// installDNSPort moves aardvark-dns to the port: it writes the drop-in when
// no port is configured yet and restarts podman's API service, which reads
// its configuration once at start. A server already running keeps its old
// port while podman redirects to the new one, so it is stopped and the
// containers that use it are restarted: the first one starts it where it
// belongs and each gets the redirect. Other containers are left alone.
func installDNSPort(ctx context.Context, w io.Writer) error {
	restart := dnsContainers(ctx)
	if dnsBindPort() == 0 {
		port := freeDNSPort()
		path := filepath.Join(containersConfDrop, dnsPortConfName)
		fmt.Fprintf(w, "$ mkdir -p %s\n", containersConfDrop)
		if err := os.MkdirAll(containersConfDrop, 0o755); err != nil {
			return err
		}
		body := "# Written by fjord: another DNS server holds port 53 on every address of\n" +
			"# this host, so podman's (aardvark-dns) listens here and netavark redirects\n" +
			"# each network gateway's port 53 to it. Remove this file to undo it.\n" +
			"[network]\ndns_bind_port = " + strconv.Itoa(port) + "\n"
		fmt.Fprintf(w, "$ cat > %s\n%s", path, body)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	if err := restartService("podman_service")(ctx, w); err != nil {
		return err
	}
	if len(restart) == 0 {
		return nil
	}
	// Not running is fine: pkill then exits 1.
	_ = runShown(ctx, w, nil, "pkill", "-x", "aardvark-dns")
	var failed []string
	for _, c := range restart {
		if err := runShown(ctx, w, nil, "podman", "restart", c); err != nil {
			failed = append(failed, c)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not restart %s -- restart them from their stacks", strings.Join(failed, ", "))
	}
	return nil
}
