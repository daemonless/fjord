package doctor

import (
	"context"
	"os/exec"
	"strings"
)

// osChecks are the Linux host's own: its firewall, which on Fedora Server
// (firewalld) blocked fjord's page from the LAN with nothing saying why.
func osChecks(cfg Config) []Check {
	port := listenPort()
	return []Check{{
		ID: "dhcp-proxy", Name: "DHCP for LAN networks", Group: "networking", HostOnly: true, Optional: true,
		Probe: func(ctx context.Context) (Status, string) {
			out, _ := exec.CommandContext(ctx, "systemctl", "is-active", "netavark-dhcp-proxy.socket").Output()
			if strings.TrimSpace(string(out)) == "active" {
				return OK, "netavark-dhcp-proxy.socket is active"
			}
			return Warn, "netavark-dhcp-proxy.socket is not active: LAN networks can only use a pool of addresses"
		},
		Why: "A LAN network whose addresses come from your DHCP server asks it through netavark's dhcp-proxy. Without it, LAN networks take addresses from a pool this host hands out.",
		Fix: "systemctl enable --now netavark-dhcp-proxy.socket",
	}, {
		ID: "firewall", Name: "host firewall", Group: "firewall", HostOnly: true, Optional: true,
		Probe: func(ctx context.Context) (Status, string) {
			if out, err := exec.CommandContext(ctx, "firewall-cmd", "--state").Output(); err == nil && strings.TrimSpace(string(out)) == "running" {
				q := exec.CommandContext(ctx, "firewall-cmd", "--query-port="+port+"/tcp").Run()
				return firewallVerdict("firewalld", q == nil, port)
			}
			if out, err := exec.CommandContext(ctx, "ufw", "status").Output(); err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Status: active") {
				return firewallVerdict("ufw", ufwAllows(string(out), port), port)
			}
			return firewallVerdict("", false, port)
		},
		Why: "fjord's page is on port " + port + ". A host firewall that does not let it in leaves the page reachable only from this machine.",
		Fix: "# firewalld (Fedora, RHEL):\nfirewall-cmd --permanent --add-port=" + port + "/tcp\nfirewall-cmd --reload\n# ufw (Debian, Ubuntu):\nufw allow " + port + "/tcp",
	}}
}
