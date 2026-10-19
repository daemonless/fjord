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
