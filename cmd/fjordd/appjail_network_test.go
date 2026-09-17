package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/hostnet"
)

// zensical's director as fjord generates it today: appjail's NAT virtualnet,
// plus a per-service expose.
const natDirector = `options:
  - virtualnet: ':<random> default'
  - nat:
services:
  zensical:
    name: zensical_zensical
    options:
      - container: 'args:--pull'
      - expose: '8000:8000 proto:tcp'
    oci:
      user: root
volumes:
  ZENSICAL_CONFIG_PATH:
    device: /containers/zensical/config
`

func seedNetwork(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	conf := `{"cniVersion":"0.4.0","name":"vlan5","plugins":[{"type":"epair","master":"vlan5bridge",
	  "ipam":{"type":"host-local","ranges":[[{"subnet":"192.168.5.0/24","gateway":"192.168.5.1"}]]}}]}`
	if err := os.WriteFile(filepath.Join(dir, "vlan5.conflist"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	old := hostnet.ConfDir
	hostnet.ConfDir = dir
	t.Cleanup(func() { hostnet.ConfDir = old })
}

// The generated options must match what was proven by hand on jupiter:
// bridge + ifconfig + defaultrouter, and NO expose -- appjail refuses
// "expose requires the following options: virtualnet" alongside a bridge.
func TestSetDirectorNetworks(t *testing.T) {
	seedNetwork(t)
	out, err := setDirectorNetworks(natDirector, "zensical", []composepkg.Attachment{{Network: "vlan5", IP: "192.168.5.222"}})
	if err != nil {
		t.Fatalf("setDirectorNetworks: %v", err)
	}
	for _, want := range []string{
		"bridge: 'epair:zensical bridge:vlan5bridge'",
		"ifconfig: 'sb_zensical:192.168.5.222/24'",
		"defaultrouter: '192.168.5.1'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, gone := range []string{"virtualnet", "nat:", "expose"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived:\n%s", gone, out)
		}
	}
	// Everything else must be preserved.
	for _, keep := range []string{"zensical_zensical", "args:--pull", "ZENSICAL_CONFIG_PATH"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q from the director:\n%s", keep, out)
		}
	}
}

func TestSetDirectorNetworksRejects(t *testing.T) {
	seedNetwork(t)
	if _, err := setDirectorNetworks(natDirector, "zensical", []composepkg.Attachment{{Network: "nosuch", IP: "192.168.5.222"}}); err == nil {
		t.Error("accepted an undefined network")
	}
	// appjail has no address pool for a network it does not manage, so an
	// address is required rather than auto-assigned.
	if _, err := setDirectorNetworks(natDirector, "zensical", []composepkg.Attachment{{Network: "vlan5"}}); err == nil {
		t.Error("accepted an empty address")
	}
}

// The name is used as "sb_<iface>", so it must be deterministic, valid, and
// short enough that the prefix still fits IFNAMSIZ.
func TestEpairName(t *testing.T) {
	cases := map[string]string{
		"zensical":                 "zensical",
		"My-Stack_01":              "mystack01",
		"averyveryverylongstackid": "averyveryver",
		"123":                      "j123",
		"!!!":                      "fjord",
	}
	for in, want := range cases {
		if got := epairName(in, 0); got != want {
			t.Errorf("epairName(%q, 0) = %q, want %q", in, got, want)
		}
	}
	for in := range cases {
		got := epairName(in, 0)
		if len("sb_"+got) > 15 {
			t.Errorf("sb_%s exceeds IFNAMSIZ", got)
		}
		if got != epairName(in, 0) {
			t.Errorf("epairName(%q, 0) is not deterministic", in)
		}
	}
}

// Two networks means two epairs, each with its own address, and exactly one
// default route -- more than one and the jail's routing table is a coin toss.
func TestSetDirectorNetworksTwo(t *testing.T) {
	seedNetwork(t)
	out, err := setDirectorNetworks(natDirector, "zensical", []composepkg.Attachment{
		{Network: "vlan5", IP: "192.168.5.222", MAC: "02:1a:2b:3c:4d:5e"},
		{Network: "vlan5", IP: "192.168.5.223"},
	})
	if err == nil {
		t.Fatal("accepted the same network twice")
	}
	out, err = setDirectorNetworks(natDirector, "zensical", []composepkg.Attachment{
		{Network: "vlan5", IP: "192.168.5.222", MAC: "02:1a:2b:3c:4d:5e"},
	})
	if err != nil {
		t.Fatalf("setDirectorNetworks: %v", err)
	}
	for _, want := range []string{
		"bridge: 'epair:zensical bridge:vlan5bridge'",
		"macaddr: 'sb_zensical:02:1a:2b:3c:4d:5e'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "defaultrouter:") != 1 {
		t.Errorf("expected exactly one default route:\n%s", out)
	}
}

// Each network gets its own wire, and the first keeps the bare name so
// existing stacks are unchanged.
func TestEpairNameIndexed(t *testing.T) {
	if got := epairName("zensical", 0); got != "zensical" {
		t.Errorf("first epair should keep the bare name, got %q", got)
	}
	if got := epairName("zensical", 1); got != "zensical1" {
		t.Errorf("second epair should be suffixed, got %q", got)
	}
	// "sb_" + name must still fit IFNAMSIZ.
	long := epairName("averylongstackname", 2)
	if len(long) > ifaceMax || !strings.HasSuffix(long, "2") {
		t.Errorf("long name not trimmed around its suffix: %q", long)
	}
}
