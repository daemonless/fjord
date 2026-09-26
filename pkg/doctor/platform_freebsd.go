//go:build freebsd

package doctor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/engine/appjail"
)

// platform describes FreeBSD: mode via the jail sysctl (an OCI-deployed fjordd
// is a jail), installs via pkg(8). A Python package is named by its port
// origin (sysutils/podman-compose): its package name carries the Python
// flavor (py312-) and changes on every flavor bump.
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
			Probe:    socketProbe,
			Install:  enableService("podman_service", "podman_service_enable"),
			Commands: serviceCommands("podman_service", "podman_service_enable"),
			Action:   "Start podman's API service",
			Why:      "fjord talks to podman over its API socket for status, logs and shells. Without it the podman engine is blind, even with podman installed.",
			Fix:      "sysrc podman_service_enable=YES\nservice podman_service onerestart",
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
			Fix:   "pkg install -y sysutils/podman-compose",
			Pkg:   map[string]string{"freebsd": "sysutils/podman-compose"},
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
			Group: "networking", Optional: true,
			Probe:    epairProbe,
			Install:  installEpair,
			Commands: epairCommands(),
			Action:   "Download the cni-epair plugin (" + epairVersion + ")",
			Why:      "The CNI plugin that gives a container its own address on your LAN instead of a port published on the host. Optional: stacks run without it, but no podman network can hand out a LAN address. appjail does not use it -- it makes its own epair.",
			Fix:      "# not in the ports tree yet; fjord's Install button does this too:\nfetch -o /usr/local/libexec/cni/epair " + epairURL + "\nchmod 755 /usr/local/libexec/cni/epair",
		},
		{
			ID: "dnsname", Name: "container name resolution (dnsname plugin)", Engine: "podman", HostOnly: true,
			Group: "networking",
			Probe: dnsnameProbe,
			Why:   "Without it, containers on the same network cannot resolve each other by name -- a stack's app looks up its database, gets nothing, and crash-loops with the networking apparently fine. Every multi-service app needs it unless its parts address each other by IP.",
			Fix:   "pkg install -y cni-dnsname",
			Pkg:   map[string]string{"freebsd": "cni-dnsname"},
		},
		{
			ID: "appjail-dns", Name: "jail name resolution (appjail-dns)", Engine: "appjail", HostOnly: true,
			Group:    "networking",
			Probe:    appjailDNSProbe,
			Install:  enableService("appjail-dns", "appjail_dns_enable"),
			Commands: serviceCommands("appjail-dns", "appjail_dns_enable"),
			Action:   "Start appjail-dns",
			Why:      "The appjail side of the same thing: jails on one network reach each other by name only while appjail-dns is running. Without it a director project's services have to address each other by IP.",
			Fix:      "sysrc appjail_dns_enable=YES\nservice appjail-dns start",
		},
		{
			ID: "appjail-git", Name: "git, for makejails on GitHub", Engine: "appjail", HostOnly: true,
			Group: "extras", Optional: true,
			Probe: appjailGitProbe(stacksDir(cfg)),
			Why:   "A director service with makejail: gh+Owner/repo (every AppJail-makejails README) is cloned from GitHub when it builds. Without git the build fails with \"git(1) is not installed\" and the jail is never created.",
			Fix:   "pkg install -y git",
		},
		{
			ID: "appjail-secrets", Name: "rage-encryption, for AppJail secrets", Engine: "appjail", HostOnly: true,
			Group: "extras", Optional: true,
			Probe: appjailSecretsProbe(stacksDir(cfg)),
			Why:   "A director service with secret: mounts AppJail secrets, which are encrypted with rage. The package is rage-encryption: `pkg install rage` installs an unrelated video player.",
			Fix:   "pkg install -y rage-encryption\nappjail secrets init",
		},
		{
			ID: "pf", Name: "pf firewall", Engine: "podman",
			Group: "firewall",
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
			Fix:   "pkg install -y appjail sysutils/py-director\n# older than 5.5.0? quarterly packages can lag behind; newer is in the\n# latest package branch, or: make -C /usr/ports/sysutils/appjail install clean",
			Pkg:   map[string]string{"freebsd": "appjail sysutils/py-director"},
			Installed: func() bool {
				_, a := exec.LookPath("appjail")
				_, d := exec.LookPath("appjail-director")
				return a == nil && d == nil
			},
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
		ID: "appjail-pf", Name: "pf anchors for AppJail", Engine: "appjail", Group: "firewall",
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
	ifaces := natIfaces()
	rules := []string{
		`rdr-anchor "cni-rdr/*"`,
		`nat-anchor "cni-rdr/*"`,
		`table <cni-nat>`,
	}
	for _, ifc := range ifaces {
		rules = append(rules, natRule(ifc))
	}
	which := "containers leave by " + strings.Join(ifaces, " and ") + ", the interfaces with an address"
	conf, _ := os.ReadFile("/etc/pf.conf")
	if strings.Contains(string(conf), "cni-rdr") {
		if miss := missingNat(string(conf), ifaces); len(miss) > 0 {
			var add []string
			for _, ifc := range miss {
				add = append(add, natRule(ifc))
			}
			return "# /etc/pf.conf NATs containers out of only some of this host's networks -- add " + strings.Join(miss, ", ") + "\n" +
				pfInsertSnippet(add, "/etc/pf.conf") + pfValidate +
				"pfctl -f /etc/pf.conf"
		}
		// The lines are shown as comments here so pasting the block can't
		// append a second copy.
		s := "# /etc/pf.conf already has podman's anchors; it must contain these (" + which + "):\n"
		for _, r := range rules {
			s += "#   " + r + "\n"
		}
		return s +
			"# they are just not loaded -- validate, then reload the ruleset\n" +
			pfValidate +
			"pfctl -f /etc/pf.conf\n" +
			"# pf not enabled at all?\n" +
			"sysrc pf_enable=YES\n" +
			"service pf start"
	}
	return "# /etc/pf.conf is missing podman's anchors -- add them once (" + which + ")\n" +
		pfInsertSnippet(rules, "/etc/pf.conf") + pfValidate +
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
//
// A fresh host has no pf.conf at all: awk cannot read it, the && skips the mv,
// and every later line fails on the missing file. touch makes it an empty one,
// which awk ends up appending to -- and makes the podman and AppJail snippets
// safe to paste in either order.
//
// Only lines the file lacks are added, so pasting it again changes nothing
// (an earlier version appended every line on each run: six copies after a few
// tries), and a file with some of the lines gets just the rest.
func pfInsertSnippet(rules []string, path string) string {
	add := strings.ReplaceAll(strings.Join(rules, "\\n"), "'", "'\\''")
	return "touch " + path + "\n" +
		"awk -v add='" + add + "' '\n" +
		"  BEGIN { n = split(add, want, \"\\n\") }\n" +
		"  { have[$0] = 1; line[NR] = $0 }\n" +
		"  END {\n" +
		"    for (i = 1; i <= n; i++) if (!(want[i] in have)) miss = miss want[i] \"\\n\"\n" +
		"    for (j = 1; j <= NR; j++) {\n" +
		"      if (miss != \"\" && line[j] ~ /^(pass|block|match|antispoof|anchor)[[:space:]]/) { printf \"%s\", miss; miss = \"\" }\n" +
		"      print line[j]\n" +
		"    }\n" +
		"    printf \"%s\", miss\n" +
		"  }' " + path + " > " + path + ".new && mv " + path + ".new " + path + "\n"
}

// pfValidate checks /etc/pf.conf before it is loaded. pfctl needs the pf
// module even for -n ("Failed to open netlink" on 15.x), and on a host that
// never ran pf it is not loaded until `service pf start`.
const pfValidate = "kldload -n pf\n" +
	"pfctl -nf /etc/pf.conf   # validate before loading\n"

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
		return s + "# they are just not loaded -- validate, then reload the ruleset\n" + pfValidate + "pfctl -f /etc/pf.conf\n# harmless if pf is already enabled and running:\nsysrc pf_enable=YES\nservice pf start"
	}
	return "# /etc/pf.conf is missing AppJail's anchors -- add them once\n" +
		pfInsertSnippet(rules, "/etc/pf.conf") + pfValidate +
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
	if miss := missingNat(string(out), natIfaces()); len(miss) > 0 {
		return Warn, "bridge-network containers are not NATed out of " + strings.Join(miss, ", ") +
			" -- anything they reach that way (the LAN, its DNS server) never answers"
	}
	return OK, "pf loaded, container rdr anchors active"
}

// natIfaces are the interfaces bridge-network containers can leave by: every
// one with an IPv4 address, the default route's first. NAT on the default
// route's interface alone broke a two-network host completely: traffic to the
// other network (its DNS server included) left un-NATed and was never answered.
func natIfaces() []string {
	out, err := exec.Command("ifconfig", "-l", "inet").Output()
	if err != nil {
		return []string{defaultIface()}
	}
	var skip []string
	if _, err := exec.LookPath("appjail"); err == nil {
		// AppJail names each virtual network's bridge after the network.
		nets, _ := exec.Command("appjail", "network", "list", "-HIp", "name").Output()
		skip = strings.Fields(string(nets))
	}
	if got := natCandidates(strings.Fields(string(out)), defaultIface(), skip); len(got) > 0 {
		return got
	}
	return []string{defaultIface()}
}

// natCandidates keeps the host's own interfaces -- not loopback, pf's, or the
// bridges, epairs and vnets that belong to containers and jails -- with def
// first.
func natCandidates(ifaces []string, def string, skip []string) []string {
	var out []string
	for _, ifc := range ifaces {
		if slices.Contains(skip, ifc) {
			continue
		}
		own := true
		for _, p := range []string{"lo", "cni-", "epair", "vnet", "pflog", "pfsync"} {
			if strings.HasPrefix(ifc, p) {
				own = false
			}
		}
		if !own {
			continue
		}
		if ifc == def {
			out = append([]string{ifc}, out...)
		} else {
			out = append(out, ifc)
		}
	}
	return out
}

func natRule(ifc string) string {
	return "nat on " + ifc + " inet from <cni-nat> to any -> (" + ifc + ")"
}

// missingNat are the interfaces with no "nat on <ifc> " rule in rules
// (pfctl -s nat output, or pf.conf).
func missingNat(rules string, ifaces []string) []string {
	var miss []string
	for _, ifc := range ifaces {
		if !strings.Contains(rules, "nat on "+ifc+" ") {
			miss = append(miss, ifc)
		}
	}
	return miss
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

// stacksDir is where fjord keeps its stacks, for checks that only matter when
// a stack uses something.
func stacksDir(cfg Config) string {
	if d := os.Getenv("FJORD_STACKS_DIR"); d != "" {
		return d
	}
	return filepath.Join(cfg.FjordRoot, "stacks")
}

// directorSpecsMatching names the stacks whose appjail-director.yml matches re.
func directorSpecsMatching(dir string, re *regexp.Regexp) []string {
	var out []string
	specs, _ := filepath.Glob(filepath.Join(dir, "*", "appjail-director.yml"))
	for _, f := range specs {
		if b, err := os.ReadFile(f); err == nil && re.Match(b) {
			out = append(out, filepath.Base(filepath.Dir(f)))
		}
	}
	return out
}

var (
	gitMakejail = regexp.MustCompile(`(?m)^\s*makejail:\s*['"]?(gh|git|gitlab)\+`)
	usesSecret  = regexp.MustCompile(`(?m)^\s*-\s*secret:`)
)

// appjailGitProbe warns only when a stack needs git and it is missing -- a
// host that never builds from GitHub has no reason to install it.
func appjailGitProbe(dir string) func(context.Context) (Status, string) {
	return func(context.Context) (Status, string) {
		if p, err := exec.LookPath("git"); err == nil {
			return OK, p
		}
		if need := directorSpecsMatching(dir, gitMakejail); len(need) > 0 {
			return Warn, strings.Join(need, ", ") + " builds from a makejail on GitHub, and git is not installed"
		}
		return OK, "not installed; only needed for gh+/git+ makejails, and no stack uses one"
	}
}

// appjailSecretsProbe warns only when a stack mounts secrets and rage-keygen
// (rage-encryption) is missing, and says so when the rage that IS installed
// is the video player.
func appjailSecretsProbe(dir string) func(context.Context) (Status, string) {
	return func(context.Context) (Status, string) {
		if p, err := exec.LookPath("rage-keygen"); err == nil {
			return OK, p
		}
		need := directorSpecsMatching(dir, usesSecret)
		if len(need) == 0 {
			return OK, "not installed; only needed for AppJail secrets, and no stack uses them"
		}
		msg := strings.Join(need, ", ") + " uses AppJail secrets, and rage-encryption is not installed"
		if _, err := exec.LookPath("rage"); err == nil {
			msg += " (the rage that is installed is the EFL video player)"
		}
		return Warn, msg
	}
}

// enableService is a check's Fix for an rc service, run for the operator:
// enabled at boot, then (re)started now. The rc script comes with the
// package, so without it the package is what is missing.
func enableService(service, rcvar string) func(context.Context, io.Writer) error {
	return func(ctx context.Context, w io.Writer) error {
		if _, err := os.Stat("/usr/local/etc/rc.d/" + service); err != nil {
			return fmt.Errorf("%s is not installed yet -- install it first", service)
		}
		if err := runShown(ctx, w, nil, "sysrc", rcvar+"=YES"); err != nil {
			return err
		}
		return runShown(ctx, w, nil, "service", service, "onerestart")
	}
}

// serviceCommands are enableService's commands, for the setup page.
func serviceCommands(service, rcvar string) []string {
	return []string{"sysrc " + rcvar + "=YES", "service " + service + " onerestart"}
}
