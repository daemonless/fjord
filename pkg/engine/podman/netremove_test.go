package podman

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/hostnet"
)

// A LAN network's conflist is removed as a file, and so is one that no
// longer parses: an invisible network is one nobody can delete.
func TestRemoveNetworkConflist(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(hostnet.UseConflists(dir))
	name := ""
	b := backendServer(t, &name)
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
