package hostnet

import (
	"os"
	"path/filepath"
	"testing"
)

// The container's side of the pair, from libcni's result cache: epair
// reports the host side too, and that MAC is not the one DHCP sees.
func TestMACOf(t *testing.T) {
	dir := t.TempDir()
	old := cniResultsDir
	cniResultsDir = dir
	t.Cleanup(func() { cniResultsDir = old })
	res := `{"ifName":"eth0","result":{"interfaces":[{"mac":"58:9c:fc:10:de:8a","name":"epair6a"},{"mac":"58:9c:fc:10:cd:c0","name":"eth0"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "lan-dhcp-abc123-eth0"), []byte(res), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := MACOf("lan-dhcp", "abc123"); got != "58:9c:fc:10:cd:c0" {
		t.Errorf("MACOf = %q", got)
	}
	if got := MACOf("lan", "abc123"); got != "" {
		t.Errorf("another network's result was read: %q", got)
	}
}

// A DHCP lease is recorded in the result too: which network it came from is
// known, even when another network shares its segment.
func TestResultAddressOf(t *testing.T) {
	dir := t.TempDir()
	old := cniResultsDir
	cniResultsDir = dir
	t.Cleanup(func() { cniResultsDir = old })
	res := `{"ifName":"eth1","result":{"ips":[{"address":"192.168.4.114/24","gateway":"192.168.4.1","interface":1}]}}`
	if err := os.WriteFile(filepath.Join(dir, "lan-dhcp-abc123-eth1"), []byte(res), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResultAddressOf("lan-dhcp", "abc123"); got != "192.168.4.114" {
		t.Errorf("ResultAddressOf = %q", got)
	}
	if got := ResultAddressOf("lan", "abc123"); got != "" {
		t.Errorf("another network's result was read: %q", got)
	}
}
