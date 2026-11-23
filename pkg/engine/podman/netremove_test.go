package podman

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/hostnet"
)

// A LAN network's conflist is removed as a file, and so is one that no
// longer parses: an invisible network is one nobody can delete.
func TestRemoveNetworkConflist(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(hostnet.UseConflists(dir))
	// On the CNI backend, whatever the OS the test runs on.
	name := backendCNI
	b := backendServer(t, &name)
	b.networkBackend(context.Background())
	t.Cleanup(hostnet.UseConflists(dir)) // backendServer pinned its own dir

	lan := filepath.Join(dir, "lan.conflist")
	os.WriteFile(lan, []byte(`{"plugins":[{"type":"epair","master":"lanbr","ipam":{"type":"dhcp"}}]}`), 0o644)
	if err := b.RemoveNetwork(context.Background(), "lan", true); err != nil {
		t.Fatalf("epair conflist: %v", err)
	}
	if _, err := os.Stat(lan); err == nil {
		t.Error("the conflist is still there")
	}

	broken := filepath.Join(dir, "broken.conflist")
	os.WriteFile(broken, []byte(`{not json`), 0o644)
	if err := b.RemoveNetwork(context.Background(), "broken", true); err != nil {
		t.Fatalf("unparseable conflist: %v", err)
	}
	if _, err := os.Stat(broken); err == nil {
		t.Error("the unparseable conflist is still there")
	}

	err := b.RemoveNetwork(context.Background(), "nosuch", true)
	if err == nil || !strings.Contains(err.Error(), "no network") {
		t.Errorf("a network that is not defined: %v", err)
	}
}

func TestBridgeMembers(t *testing.T) {
	out := "cni-podman1: flags=8843<UP,BROADCAST,RUNNING,SIMPLEX,MULTICAST> metric 0 mtu 1500\n" +
		"\tinet 10.134.0.1 netmask 0xffffff00 broadcast 10.134.0.255\n" +
		"\tmember: vnet41e6e6bb flags=143<LEARNING,DISCOVER,AUTOEDGE,AUTOPTP>\n" +
		"\t        ifmaxaddr 0 port 7 priority 128 path cost 2000\n" +
		"\tmember: vnet710ab722 flags=143<LEARNING,DISCOVER,AUTOEDGE,AUTOPTP>\n"
	got := bridgeMembers(out)
	if len(got) != 2 || got[0] != "vnet41e6e6bb" || got[1] != "vnet710ab722" {
		t.Errorf("got %v", got)
	}
}

// The files CNI's DNS helper left for a network go with it; a pid that is
// not that helper is left alone.
func TestRetireCNIDNS(t *testing.T) {
	old := cniDNSDir
	cniDNSDir = t.TempDir()
	t.Cleanup(func() { cniDNSDir = old })
	dir := filepath.Join(cniDNSDir, "t-net")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// This test's own pid: running, and not a dnsmasq for this network.
	if err := os.WriteFile(filepath.Join(dir, "pidfile"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	retireCNIDNS(context.Background(), "t-net")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the network's DNS files are still there: %v", err)
	}
	retireCNIDNS(context.Background(), "t-none") // nothing to do, no panic
}
