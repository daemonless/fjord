package doctor

import (
	"net"
	"os"
	"strings"
)

// listenPort is the port fjord's page answers on (FJORD_LISTEN, ":3567").
func listenPort() string {
	addr := os.Getenv("FJORD_LISTEN")
	if addr == "" {
		return "3567"
	}
	if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		return port
	}
	return "3567"
}

// firewallVerdict reads a host firewall's answer about port: which firewall
// is running ("firewalld", "ufw", or "" for none), and whether port is let in.
func firewallVerdict(which string, allowed bool, port string) (Status, string) {
	switch {
	case which == "":
		return OK, "no host firewall running"
	case allowed:
		return OK, which + " lets port " + port + " in"
	default:
		return Warn, which + " is running and does not let port " + port + " in: fjord's page answers only on this host"
	}
}

// ufwAllows reports whether `ufw status` output lets port in.
func ufwAllows(status, port string) bool {
	for _, l := range strings.Split(status, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && (f[0] == port || strings.HasPrefix(f[0], port+"/")) && f[1] == "ALLOW" {
			return true
		}
	}
	return false
}
