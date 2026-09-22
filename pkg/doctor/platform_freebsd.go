//go:build freebsd

package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/engine/appjail"
)

// platform describes FreeBSD: mode via the jail sysctl (an OCI-deployed fjordd
// is a jail), installs via pkg(8). Package names here track the ports tree --
// the podman-compose name is Python-flavor-dependent and moves on flavor bumps.
func platform(cfg Config) platformInfo {
	mode := "host"
	if engine.Jailed() {
		mode = "container"
	}
	_, pkgErr := exec.LookPath("pkg")
	checks := []Check{
		{
			ID: "podman", Name: "podman CLI", Engine: "podman",
			Probe: binProbe("podman"),
			Why:   "Runs the containers. Every stack on the podman engine is created, started and inspected through it.",
			Fix:   "pkg install -y podman",
			Pkg:   map[string]string{"freebsd": "podman"},
		},
		{
			ID: "socket", Name: "podman API socket", Engine: "podman",
			Probe: socketProbe,
			Why:   "fjord talks to podman over its API socket for status, logs and shells. Without it the podman engine is blind, even with podman installed.",
			Fix:   "sysrc podman_service_enable=YES\nservice podman_service onerestart",
		},
		{
			ID: "podman-stale", Name: "podman API service is current", Engine: "podman", HostOnly: true,
			Probe: podmanServiceStaleProbe,
			Why:   "`pkg upgrade` replaces podman and its runtime under a service that keeps running, and the old process cannot spawn an exec with binaries that moved. Everything keeps working except the shell, which fails with \"no such file or directory\" -- on a host where the CLI works perfectly, so it reads as a fjord bug. Nothing else notices, because starting and stopping stacks is unaffected.",
			Fix:   "service podman_service restart",
		},
		{
			ID: "compose", Name: "podman-compose", Engine: "podman",
			Probe: binProbe("podman-compose"),
			Why:   "Turns a stack's compose.yaml into containers. fjord hands it the file on every up, down and update.",
			Fix:   "pkg install -y py312-podman-compose",
			Pkg:   map[string]string{"freebsd": "py312-podman-compose"},
		},
		{
			ID: "conmon", Name: "conmon", Engine: "podman", HostOnly: true,
			Probe: binProbe("conmon"),
			Why:   "The per-container monitor podman starts everything through. It holds a container's I/O and exit status while podman itself isn't running.",
			Fix:   "pkg install -y conmon",
			Pkg:   map[string]string{"freebsd": "conmon"},
		},
		{
			ID: "ocijail", Name: "ocijail runtime", Engine: "podman", HostOnly: true,
			Probe: ocijailProbe,
			Why:   "The OCI runtime that turns a container into a FreeBSD jail; podman cannot start anything without it. 0.6.0+ fixes a umask leak that left files in built images root-only.",
			Fix:   "pkg install -y ocijail\n# already installed but older than 0.6.0?\npkg upgrade -y ocijail",
			Pkg:   map[string]string{"freebsd": "ocijail"},
		},
		{
			ID: "epair", Name: "LAN networks (epair plugin)", Engine: "podman", HostOnly: true,
			Probe: epairProbe,
			Why:   "The CNI plugin that gives a container its own address on your LAN instead of a port published on the host. Optional: stacks run without it, but no podman network can hand out a LAN address. appjail does not use it -- it makes its own epair.",
			Fix:   "# not in the ports tree yet:\nfetch -o /usr/local/libexec/cni/epair https://raw.githubusercontent.com/daemonless/cni-epair/main/epair\nchmod 755 /usr/local/libexec/cni/epair",
		},
		{
			ID: "dnsname", Name: "container name resolution (dnsname plugin)", Engine: "podman", HostOnly: true,
			Probe: dnsnameProbe,
			Why:   "Without it, containers on the same network cannot resolve each other by name -- a stack's app looks up its database, gets nothing, and crash-loops with the networking apparently fine. Every multi-service app needs it unless its parts address each other by IP.",
			Fix:   "pkg install -y cni-dnsname",
			Pkg:   map[string]string{"freebsd": "cni-dnsname"},
		},
		{
			ID: "appjail-dns", Name: "jail name resolution (appjail-dns)", Engine: "appjail", HostOnly: true,
			Probe: appjailDNSProbe,
			Why:   "The appjail side of the same thing: jails on one network reach each other by name only while appjail-dns is running. Without it a director project's services have to address each other by IP.",
			Fix:   "sysrc appjail_dns_enable=YES\nservice appjail-dns start",
		},
		{
			ID: "pf", Name: "pf firewall", Engine: "podman",
			Probe: pfProbe,
			Why:   "podman's bridge networking publishes ports through pf's rdr/nat anchors. If they aren't loaded, containers start fine but their published ports hang. Host-network stacks don't need this.",
			Fix:   pfFix(),
		},
		{
			// Engine "" so it always shows -- the appjail engine is optional
			// but its version affects which apps run there.
			ID: "appjail", Name: "AppJail engine",
			Probe: appjailProbe,
			Why:   "Optional second engine: runs apps as native FreeBSD jails from the same OCI images, orchestrated by appjail-director. 5.5.0+ is needed for kernel modules, secrets and full OCI support.",
			Fix:   "pkg install -y appjail sysutils/py-director\n# already installed but older than 5.5.0?\npkg upgrade -y appjail",
		},
		rootCheck(cfg.FjordRoot),
	}
	// Only meaningful where appjail exists; a podman-only host has nothing to
	// check and shouldn't see an amber row for it.
	if _, err := exec.LookPath("appjail"); err == nil {
		checks = append(checks, appjailPfCheck())
	}
	return platformInfo{os: "freebsd", mode: mode, canInstall: pkgErr == nil, checks: checks}
}

// appjailPfCheck is the pf-anchors check for AppJail's virtual networks.
func appjailPfCheck() Check {
	return Check{
		ID: "appjail-pf", Name: "pf anchors for AppJail",
		Probe: appjailPfProbe,
		Why:   "AppJail gives each jail a virtual-network address and NATs it through pf's appjail-nat/appjail-rdr anchors. Without them every jail on a virtual network fails to create (\"The nat command requires appjail-nat/jail/* ...\"). Host-network jails don't need this.",
		Fix:   appjailPfFix(),
	}
}

// pfFix is the pf remediation, built from what /etc/pf.conf actually
// contains: if podman's cni-rdr anchors are already there the rules were only
// flushed or pf is off, so the fix is a reload/enable; if they're missing, the
// fix is the exact lines to add (with this host's default-route interface),
// then enable -- "reload pf" is no help to someone who never had the rules.
func pfFix() string {
	ifc := defaultIface()
	rules := []string{
		`rdr-anchor "cni-rdr/*"`,
		`nat-anchor "cni-rdr/*"`,
		`table <cni-nat>`,
		"nat on " + ifc + " inet from <cni-nat> to any -> (" + ifc + ")",
	}
	conf, _ := os.ReadFile("/etc/pf.conf")
	if strings.Contains(string(conf), "cni-rdr") {
		// The lines are shown as comments here so pasting the block can't
		// append a second copy.
		s := "# /etc/pf.conf already has podman's anchors; it must contain these (" + ifc + " = your LAN interface):\n"
		for _, r := range rules {
			s += "#   " + r + "\n"
		}
		return s +
			"# they are just not loaded -- validate, then reload the ruleset\n" +
			"pfctl -nf /etc/pf.conf\n" +
			"pfctl -f /etc/pf.conf\n" +
			"# pf not enabled at all?\n" +
			"sysrc pf_enable=YES\n" +
			"service pf start"
	}
	return "# /etc/pf.conf is missing podman's anchors -- add them once (" + ifc + " = your LAN interface)\n" +
		pfInsertSnippet(rules) +
		"sysrc pf_enable=YES\n" +
		"service pf start\n" +
		"# and load them now: `service pf start` does nothing if pf was already up\n" +
		"pfctl -f /etc/pf.conf"
}

// pfInsertSnippet returns shell that adds translation rules to /etc/pf.conf in
// the right place: before the first filter rule (pass/block/match/antispoof/
// anchor), or at the end when there are none. pf rejects the whole file when a
// nat/rdr anchor follows a filter rule ("Rules must be in order") and the rc
// script then leaves pf running with NOTHING loaded -- appending at the end
// did exactly that on a host with pass rules. The load is validated first so
// a mistake is shown instead of silently unloading the firewall.
func pfInsertSnippet(rules []string) string {
	add := strings.ReplaceAll(strings.Join(rules, "\\n"), "'", "'\\''")
	return "awk -v add='" + add + "' '\n" +
		"  !done && /^(pass|block|match|antispoof|anchor)[[:space:]]/ { print add; done=1 } { print }\n" +
		"  END { if (!done) print add }' /etc/pf.conf > /etc/pf.conf.new && mv /etc/pf.conf.new /etc/pf.conf\n" +
		"pfctl -nf /etc/pf.conf   # validate before loading\n"
}

// appjailPfProbe checks pf carries AppJail's NAT/rdr anchors, which its
// virtual-network jails need (the appjail-director default). Mirrors pfProbe.
func appjailPfProbe(ctx context.Context) (Status, string) {
	if _, err := exec.LookPath("kldstat"); err != nil {
		return Unknown, "kldstat not available here to check pf"
	}
	if exec.CommandContext(ctx, "kldstat", "-q", "-m", "pf").Run() != nil {
		return Warn, "pf is not loaded -- virtual-network jails cannot be created; host-network jails are unaffected"
	}
	out, err := exec.CommandContext(ctx, "pfctl", "-s", "nat").Output()
	if err != nil {
		return Unknown, "pf loaded; cannot read the ruleset to verify the appjail anchors (pfctl needs root)"
	}
	if !strings.Contains(string(out), "appjail-nat") {
		return Warn, "pf is loaded but the appjail-nat anchors are not in the active ruleset -- creating a jail fails with \"The nat command requires appjail-nat/jail/* ...\""
	}
	return OK, "pf loaded, appjail-nat anchors active"
}

// appjailPfFix is the remediation for appjailPfProbe, built from what
// /etc/pf.conf contains: reload when the anchors are there, else the exact
// lines to add (the daemonless.io quick-start ones).
func appjailPfFix() string {
	rules := []string{
		`nat-anchor "appjail-nat/jail/*"`,
		`nat-anchor "appjail-nat/network/*"`,
		`rdr-anchor "appjail-rdr/*"`,
	}
	conf, _ := os.ReadFile("/etc/pf.conf")
	if strings.Contains(string(conf), "appjail-nat") {
		s := "# /etc/pf.conf already has AppJail's anchors; it must contain these:\n"
		for _, r := range rules {
			s += "#   " + r + "\n"
		}
		return s + "# they are just not loaded -- validate, then reload the ruleset\npfctl -nf /etc/pf.conf\npfctl -f /etc/pf.conf\n# harmless if pf is already enabled and running:\nsysrc pf_enable=YES\nservice pf start"
	}
	return "# /etc/pf.conf is missing AppJail's anchors -- add them once\n" +
		pfInsertSnippet(rules) +
		"sysrc pf_enable=YES\nservice pf start\n# and load them now: `service pf start` does nothing if pf was already up\npfctl -f /etc/pf.conf"
}

// defaultIface returns the interface carrying the default route (the one
// podman's NAT should masquerade behind), or a placeholder to edit.
func defaultIface() string {
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) == 2 && f[0] == "interface:" {
				return f[1]
			}
		}
	}
	return "$ext_if"
}

// appjailProbe reports the appjail engine's readiness. It's optional (podman
// is the default), so "not installed" is neutral (Unknown); an installed but
// pre-5.5.0 appjail Warns since it runs simple apps but lacks the OCI maturity
// (load-kld, secrets) that advanced apps need.
func appjailProbe(ctx context.Context) (Status, string) {
	if _, err := exec.LookPath("appjail"); err != nil {
		return Unknown, "not installed (optional -- enables the appjail engine)"
	}
	// fjord runs appjail stacks through appjail-director (sysutils/py-director);
	// appjail alone gets the engine listed but every Start fails.
	if _, err := exec.LookPath("appjail-director"); err != nil {
		return Warn, "appjail installed but appjail-director is missing -- the appjail engine stays disabled until it is"
	}
	ver := appjail.Version() // memoized; `appjail version` itself takes seconds
	if ver == "" {
		return Warn, "installed but its version could not be read"
	}
	if versionBelow(ver, 5, 5) {
		return Warn, "AppJail " + ver + " -- 5.5.0+ recommended; older may not run kernel-module apps, secrets, or every OCI app"
	}
	return OK, ver
}

// ocijailProbe checks the ocijail OCI runtime is present and >= 0.6.0. podman
// can't run a container without it, so missing Fails. Older ocijail runs but
// carries correctness bugs (the create.cpp umask leak that makes built files
// root-only, plus OCI-spec gaps), so a pre-0.6.0 build Warns with an upgrade.
// epairProbe reports the LAN-network plugin. Warn, never Fail: a host without
// it runs every stack perfectly well on published ports -- it just cannot give
// one an address of its own.
func epairProbe(ctx context.Context) (Status, string) {
	const path = "/usr/local/libexec/cni/epair"
	if _, err := os.Stat(path); err != nil {
		return Warn, "not installed -- containers can only use published ports"
	}
	// Installed: say whether it can take a DHCP lease, because a network on a
	// segment with a DHCP server is the common case and an older plugin
	// silently cannot do it.
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = append(os.Environ(), "CNI_COMMAND=FEATURES")
	out, err := cmd.Output()
	if err == nil && strings.Contains(string(out), "dhcp") {
		return OK, "installed, with DHCP support"
	}
	return OK, "installed; no DHCP support -- networks on it need an address range"
}

func ocijailProbe(ctx context.Context) (Status, string) {
	if _, err := exec.LookPath("ocijail"); err != nil {
		return Fail, "ocijail not found in PATH"
	}
	ver := ocijailVersion(ctx)
	if ver == "" {
		return Warn, "installed, but its version could not be read -- 0.6.0+ required"
	}
	if versionBelow(ver, 0, 6) {
		return Warn, "ocijail " + ver + " -- 0.6.0+ required; older leaks umask into built images and lacks OCI fixes"
	}
	return OK, "ocijail " + ver
}

// ocijailVersion returns the runtime's dotted version ("0.6.1") or "" if it
// can't be read. pkg's database is asked first: the binary's own
// `ocijail --version` string lags releases (0.6.1 still prints "0.6.0"), so a
// current package could otherwise read as below minimum. The CLI is the
// fallback for a non-pkg install.
func ocijailVersion(ctx context.Context) string {
	if out, err := exec.CommandContext(ctx, "pkg", "query", "%v", "ocijail").Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			return strings.SplitN(v, "_", 2)[0] // drop the port revision
		}
	}
	out, err := exec.CommandContext(ctx, "ocijail", "--version").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	for i := len(fields) - 1; i >= 0; i-- {
		if f := fields[i]; f != "" && f[0] >= '0' && f[0] <= '9' {
			return f
		}
	}
	return ""
}

// versionBelow reports whether dotted version v is older than major.minor.
func versionBelow(v string, major, minor int) bool {
	parts := strings.SplitN(v, ".", 3)
	num := func(s string) int {
		n := 0
		for _, c := range s {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	if len(parts) == 0 || parts[0] == "" {
		return false // unknown version -> don't warn
	}
	if maj := num(parts[0]); maj != major {
		return maj < major
	}
	min := 0
	if len(parts) > 1 {
		min = num(parts[1])
	}
	return min < minor
}

// pfProbe reports pf readiness in two layers: the module, then the container
// rdr anchors in the ACTIVE nat ruleset. The second is the treacherous one --
// podman writes correct per-container rdr rules into its anchors, but if the
// main ruleset was flushed (observed after a reboot) nothing evaluates them
// and every published bridge port silently blackholes while "pf loaded" still
// reads true. Warn, not fail: host-network stacks run fine without pf.
func pfProbe(ctx context.Context) (Status, string) {
	if _, err := exec.LookPath("kldstat"); err != nil {
		return Unknown, "kldstat not available here to check pf"
	}
	if exec.CommandContext(ctx, "kldstat", "-q", "-m", "pf").Run() != nil {
		return Warn, "pf is not loaded -- bridge-network stacks will fail; host-network stacks are unaffected"
	}
	out, err := exec.CommandContext(ctx, "pfctl", "-s", "nat").Output()
	if err != nil {
		return Unknown, "pf loaded; cannot read the ruleset to verify container anchors (pfctl needs root)"
	}
	if !strings.Contains(string(out), "cni-rdr") && !strings.Contains(string(out), "netavark") {
		return Warn, "pf is loaded but the container rdr anchors are not in the active ruleset -- published bridge ports will hang; reload pf.conf"
	}
	return OK, "pf loaded, container rdr anchors active"
}

// dnsnameProbe reports whether podman can resolve container names.
//
// The plugin is what writes a per-network dnsmasq and points the containers at
// it. Without it they get the HOST's resolver, so "database" resolves to
// whatever the LAN says -- usually nothing -- and a multi-service stack fails
// in a way that looks like the app rather than the host.
func dnsnameProbe(ctx context.Context) (Status, string) {
	for _, dir := range []string{"/usr/local/libexec/cni", "/usr/libexec/cni"} {
		if _, err := os.Stat(filepath.Join(dir, "dnsname")); err == nil {
			return OK, "installed"
		}
	}
	return Warn, "not installed -- services in a stack cannot find each other by name"
}

// appjailDNSProbe reports whether appjail's own name resolution is running.
// Installed but stopped is the common case: the package ships it disabled.
func appjailDNSProbe(ctx context.Context) (Status, string) {
	if _, err := os.Stat("/usr/local/etc/rc.d/appjail-dns"); err != nil {
		return Warn, "not installed -- jails cannot find each other by name"
	}
	if err := exec.CommandContext(ctx, "service", "appjail-dns", "status").Run(); err != nil {
		return Warn, "installed but not running -- jails cannot find each other by name"
	}
	return OK, "running"
}

// podmanServiceStaleProbe reports a podman API service older than the podman
// packages it runs on.
//
// Measured on jupiter 2026-09-22: `pkg upgrade` replaced podman, conmon and
// ocijail at 18:28 under a service that had been up since Sep 6. `podman exec`
// on the command line worked -- that is the new binary -- while the identical
// call through the API returned 500 "no such file or directory", so every
// stack's shell was dead and nothing else was. Restarting the service fixed
// it instantly.
//
// Compared by TIME, not by version: that upgrade was almost certainly a port
// revision bump (5.8.6 -> 5.8.6_1), so Client.Version and Server.Version read
// identical and a version check sees nothing wrong.
func podmanServiceStaleProbe(ctx context.Context) (Status, string) {
	pid := strings.TrimSpace(firstLine(run(ctx, "pgrep", "-f", "podman.*system service")))
	if pid == "" {
		return OK, "" // not running: the socket check is the one that says so
	}
	// etimes is elapsed SECONDS, which needs no date parsing and no locale.
	etimes, err := strconv.Atoi(strings.TrimSpace(firstLine(run(ctx, "ps", "-o", "etimes=", "-p", pid))))
	if err != nil {
		return OK, ""
	}
	started := time.Now().Add(-time.Duration(etimes) * time.Second)

	var newest time.Time
	var newestPkg string
	for _, name := range []string{"podman", "conmon", "ocijail"} {
		out := strings.TrimSpace(firstLine(run(ctx, "pkg", "query", "%t", name)))
		secs, err := strconv.ParseInt(out, 10, 64)
		if err != nil {
			continue
		}
		if t := time.Unix(secs, 0); t.After(newest) {
			newest, newestPkg = t, name
		}
	}
	return serviceStale(started, newest, newestPkg)
}

// serviceStale is the comparison on its own, so the rule can be tested without
// a host in the broken state.
func serviceStale(started, installed time.Time, pkg string) (Status, string) {
	if pkg == "" || !installed.After(started) {
		return OK, "running on the installed version"
	}
	return Warn, fmt.Sprintf(
		"running since %s but %s was installed %s -- shells will fail until it is restarted",
		started.Format("Jan 2 15:04"), pkg, installed.Format("Jan 2 15:04"))
}

// run is the output of a command, or "" if it fails at all. Every caller here
// treats "cannot tell" as "nothing to report".
func run(ctx context.Context, name string, args ...string) string {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
