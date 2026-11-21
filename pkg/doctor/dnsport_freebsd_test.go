package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sockstat on a host where dnsmasq answers for AppJail's jail names and
// aardvark-dns already runs on another port for one network.
const sockstatWildcard = `USER     COMMAND    PID   FD  PROTO  LOCAL ADDRESS         FOREIGN ADDRESS
root     aardvark-d 1614  11  udp4   10.89.1.1:5300        *:*
nobody   dnsmasq    1296  4   udp4   *:53                  *:*
nobody   dnsmasq    1296  6   udp6   *:53                  *:*
`

func TestWildcardDNS(t *testing.T) {
	if got := wildcardDNS(sockstatWildcard); got != "dnsmasq" {
		t.Errorf("got %q, want dnsmasq", got)
	}
	// A resolver on loopback or one address leaves the gateways free.
	specific := "USER COMMAND PID FD PROTO LOCAL FOREIGN\nunbound local-unbo 900 3 udp4 127.0.0.1:53 *:*\nroot aardvark-d 1 11 udp4 10.88.0.1:53 *:*\n"
	if got := wildcardDNS(specific); got != "" {
		t.Errorf("a server on one address reported as %q", got)
	}
	if got := wildcardDNS(""); got != "" {
		t.Errorf("empty listing: %q", got)
	}
}

func TestParseDNSBindPort(t *testing.T) {
	for conf, want := range map[string]int{
		"[network]\nnetwork_backend = \"netavark\"\ndns_bind_port = 5300\n": 5300,
		"[network]\ndns_bind_port=5301 # moved\n":                           5301,
		"# dns_bind_port = 5300\n[network]\n":                               0,
		"[engine]\ndns_bind_port = 5300\n":                                  0,
		"[network]\ndns_bind_port = 0\n":                                    0,
		"":                                                                  0,
	} {
		if got := parseDNSBindPort(conf); got != want {
			t.Errorf("%q: got %d, want %d", conf, got, want)
		}
	}
}

// Drop-ins are read in name order after containers.conf; the last one that
// sets the port wins, as podman does it.
func TestDNSBindPortDropIns(t *testing.T) {
	dir := t.TempDir()
	oldConf, oldDrop := containersConf, containersConfDrop
	containersConf, containersConfDrop = filepath.Join(dir, "containers.conf"), filepath.Join(dir, "d")
	t.Cleanup(func() { containersConf, containersConfDrop = oldConf, oldDrop })
	if got := dnsBindPort(); got != 0 {
		t.Errorf("nothing configured: %d", got)
	}
	os.MkdirAll(containersConfDrop, 0o755)
	os.WriteFile(containersConf, []byte("[network]\ndns_bind_port = 5353\n"), 0o644)
	os.WriteFile(filepath.Join(containersConfDrop, "10-a.conf"), []byte("[engine]\nruntime = \"ocijail\"\n"), 0o644)
	if got := dnsBindPort(); got != 5353 {
		t.Errorf("containers.conf only: %d", got)
	}
	os.WriteFile(filepath.Join(containersConfDrop, "50-b.conf"), []byte("[network]\ndns_bind_port = 5300\n"), 0o644)
	if got := dnsBindPort(); got != 5300 {
		t.Errorf("drop-in wins: %d", got)
	}
}

// A port set while containers ran leaves their aardvark-dns on the old one.
func TestStaleAardvarkPort(t *testing.T) {
	on53 := "root aardvark-d 54164 11 udp4 10.127.1.1:53 *:*\nnobody dnsmasq 1 4 udp4 *:53 *:*\n"
	if got := staleAardvarkPort(on53, 5300); got != 53 {
		t.Errorf("server on 53, podman told 5300: got %d", got)
	}
	if got := staleAardvarkPort(on53, 0); got != 0 {
		t.Errorf("server on 53, nothing configured: got %d", got)
	}
	if got := staleAardvarkPort(sockstatWildcard, 5300); got != 0 {
		t.Errorf("server where it should be: got %d", got)
	}
	if got := staleAardvarkPort(sockstatWildcard, 0); got != 5300 {
		t.Errorf("the setting was removed under a running server: got %d", got)
	}
	if got := staleAardvarkPort("nobody dnsmasq 1 4 udp4 *:53 *:*\n", 5300); got != 0 {
		t.Errorf("no aardvark-dns running: got %d", got)
	}
}

// The plan shown before the click says what will be restarted, and writes
// nothing when a port is already configured.
func TestDNSPortCommands(t *testing.T) {
	fresh := strings.Join(dnsPortCommands(5300, true, nil), "\n")
	if !strings.Contains(fresh, "dns_bind_port = 5300") || strings.Contains(fresh, "podman restart") {
		t.Errorf("nothing running:\n%s", fresh)
	}
	busy := strings.Join(dnsPortCommands(5300, true, []string{"app_db_1", "app_web_1"}), "\n")
	if !strings.Contains(busy, "pkill -x aardvark-dns") || !strings.Contains(busy, "podman restart app_db_1 app_web_1") {
		t.Errorf("containers to restart are not shown:\n%s", busy)
	}
	stale := strings.Join(dnsPortCommands(5300, false, []string{"app_web_1"}), "\n")
	if strings.Contains(stale, "dns_bind_port") || !strings.Contains(stale, "podman restart app_web_1") {
		t.Errorf("port already set:\n%s", stale)
	}
}
