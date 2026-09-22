package lannet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/hostnet"
)

// A conflist carrying fields the READ path cannot report: mtu, the pool range,
// and a description. engine.Network has none of them, so anything that
// regenerates the file from what a caller can see would drop them.
const v4Conflist = `{
  "cniVersion": "0.4.0",
  "name": "lan",
  "plugins": [
    {
      "type": "epair",
      "master": "lanbridge",
      "mtu": 9000,
      "capabilities": {"ips": true, "mac": true},
      "ipam": {
        "type": "host-local",
        "routes": [{"dst": "0.0.0.0/0"}],
        "ranges": [[{"subnet": "192.168.4.0/24", "gateway": "192.168.4.1",
                     "rangeStart": "192.168.4.100", "rangeEnd": "192.168.4.200"}]]
      }
    }
  ],
  "x-fjord": {"description": "the house LAN"}
}
`

func seed(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lan.conflist"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := hostnet.ConfDir
	hostnet.ConfDir = dir
	t.Cleanup(func() { hostnet.ConfDir = old })
	return filepath.Join(dir, "lan.conflist")
}

func read(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("result is not valid JSON: %v\n%s", err, raw)
	}
	return doc
}

// The whole reason this merges instead of regenerating.
func TestSetSegment6KeepsWhatTheReadPathCannotSee(t *testing.T) {
	path := seed(t, v4Conflist)
	if err := SetSegment6("lan", "fd00:4:103::/64", "fd00:4:103::1"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{`"mtu": 9000`, `"rangeStart": "192.168.4.100"`, `"the house LAN"`, `"master": "lanbridge"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("edit dropped %s:\n%s", want, raw)
		}
	}
	doc := read(t, path)
	ipam := doc["plugins"].([]any)[0].(map[string]any)["ipam"].(map[string]any)
	if got := len(ipam["ranges"].([]any)); got != 2 {
		t.Fatalf("want 2 ranges (v4 + v6), got %d", got)
	}
	// host-local needs a route per family: without ::/0 the container gets an
	// address and no way off the segment.
	var hasV6Route bool
	for _, r := range ipam["routes"].([]any) {
		if dst, _ := r.(map[string]any)["dst"].(string); dst == "::/0" {
			hasV6Route = true
		}
	}
	if !hasV6Route {
		t.Errorf("no ::/0 route: %v", ipam["routes"])
	}
}

// Running it twice must not stack up ranges.
func TestSetSegment6IsIdempotentAndRemovable(t *testing.T) {
	path := seed(t, v4Conflist)
	for i := 0; i < 3; i++ {
		if err := SetSegment6("lan", "fd00:4:103::/64", "fd00:4:103::1"); err != nil {
			t.Fatal(err)
		}
	}
	ipam := read(t, path)["plugins"].([]any)[0].(map[string]any)["ipam"].(map[string]any)
	if got := len(ipam["ranges"].([]any)); got != 2 {
		t.Fatalf("three edits produced %d ranges, want 2", got)
	}

	if err := SetSegment6("lan", "", ""); err != nil {
		t.Fatal(err)
	}
	ipam = read(t, path)["plugins"].([]any)[0].(map[string]any)["ipam"].(map[string]any)
	if got := len(ipam["ranges"].([]any)); got != 1 {
		t.Fatalf("after removal: %d ranges, want 1", got)
	}
	for _, r := range ipam["routes"].([]any) {
		if dst, _ := r.(map[string]any)["dst"].(string); dst == "::/0" {
			t.Error("the ::/0 route outlived the segment it was for")
		}
	}
}

func TestSetSegment6Rejects(t *testing.T) {
	seed(t, v4Conflist)
	for _, tc := range []struct{ subnet, gw, want string }{
		{"192.168.9.0/24", "", "not an IPv6 subnet"},
		{"fd00::/64", "192.168.4.1", "not an IPv6 address"},
		{"fd00:4:103::/64", "fd00:9::1", "not in fd00:4:103::/64"},
		{"banana", "", "not an IPv6 subnet"},
	} {
		err := SetSegment6("lan", tc.subnet, tc.gw)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("SetSegment6(%q,%q) = %v, want it to mention %q", tc.subnet, tc.gw, err, tc.want)
		}
	}
	if err := SetSegment6("nosuch", "fd00::/64", ""); err == nil {
		t.Error("accepted a network that is not defined")
	}
}

// A DHCP network's addresses come from the v4-only dhcp plugin; edit must not
// be the way around the rule the create form enforces.
func TestSetSegment6RefusesNonPoolNetworks(t *testing.T) {
	seed(t, `{"cniVersion":"0.4.0","name":"lan","plugins":[{"type":"epair","master":"lanbridge","ipam":{"type":"dhcp"}}]}`)
	err := SetSegment6("lan", "fd00:4:103::/64", "")
	if err == nil || !strings.Contains(err.Error(), "only a pool or static network") {
		t.Errorf("got %v, want a refusal naming the allocator", err)
	}
}
