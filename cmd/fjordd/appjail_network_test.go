package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
func TestSetDirectorNetwork(t *testing.T) {
	seedNetwork(t)
	out, err := setDirectorNetwork(natDirector, "zensical", "vlan5", "192.168.5.222")
	if err != nil {
		t.Fatalf("setDirectorNetwork: %v", err)
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

func TestSetDirectorNetworkRejects(t *testing.T) {
	seedNetwork(t)
	if _, err := setDirectorNetwork(natDirector, "zensical", "nosuch", "192.168.5.222"); err == nil {
		t.Error("accepted an undefined network")
	}
	// appjail has no address pool for a network it does not manage, so an
	// address is required rather than auto-assigned.
	if _, err := setDirectorNetwork(natDirector, "zensical", "vlan5", ""); err == nil {
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
		if got := epairName(in); got != want {
			t.Errorf("epairName(%q) = %q, want %q", in, got, want)
		}
	}
	for in := range cases {
		got := epairName(in)
		if len("sb_"+got) > 15 {
			t.Errorf("sb_%s exceeds IFNAMSIZ", got)
		}
		if got != epairName(in) {
			t.Errorf("epairName(%q) is not deterministic", in)
		}
	}
}
