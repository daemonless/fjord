package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
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
	// A DHCP network alongside it: every jail asks the wire for its own lease,
	// which is what lets several services share one network.
	dhcp := `{"cniVersion":"0.4.0","name":"vlan6","plugins":[{"type":"epair","master":"vlan6bridge",
	  "ipam":{"type":"dhcp"}}],"x-fjord":{"subnet":"192.168.6.0/24","gateway":"192.168.6.1"}}`
	if err := os.WriteFile(filepath.Join(dir, "vlan6.conflist"), []byte(dhcp), 0o644); err != nil {
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
	out, err := setDirectorNetworks(context.Background(), natDirector, "zensical", []composepkg.Attachment{{Network: "vlan5", IP: "192.168.5.222"}})
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
	if _, err := setDirectorNetworks(context.Background(), natDirector, "zensical", []composepkg.Attachment{{Network: "nosuch", IP: "192.168.5.222"}}); err == nil {
		t.Error("accepted an undefined network")
	}
	// appjail has no address pool for a network it does not manage, so an
	// address is required rather than auto-assigned.
	if _, err := setDirectorNetworks(context.Background(), natDirector, "zensical", []composepkg.Attachment{{Network: "vlan5"}}); err == nil {
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
		if got := epairName(in, "", 0, 0); got != want {
			t.Errorf("epairName(%q) = %q, want %q", in, got, want)
		}
	}
	for in := range cases {
		got := epairName(in, "", 0, 0)
		if len("sb_"+got) > 15 {
			t.Errorf("sb_%s exceeds IFNAMSIZ", got)
		}
		if got != epairName(in, "", 0, 0) {
			t.Errorf("epairName(%q) is not deterministic", in)
		}
	}
}

// Two networks means two epairs, each with its own address, and exactly one
// default route -- more than one and the jail's routing table is a coin toss.
func TestSetDirectorNetworksTwo(t *testing.T) {
	seedNetwork(t)
	out, err := setDirectorNetworks(context.Background(), natDirector, "zensical", []composepkg.Attachment{
		{Network: "vlan5", IP: "192.168.5.222", MAC: "02:1a:2b:3c:4d:5e"},
		{Network: "vlan5", IP: "192.168.5.223"},
	})
	if err == nil {
		t.Fatal("accepted the same network twice")
	}
	out, err = setDirectorNetworks(context.Background(), natDirector, "zensical", []composepkg.Attachment{
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
	if got := epairName("zensical", "", 0, 0); got != "zensical" {
		t.Errorf("first epair should keep the bare name, got %q", got)
	}
	if got := epairName("zensical", "", 0, 1); got != "zensical1" {
		t.Errorf("second epair should be suffixed, got %q", got)
	}
	// "sb_" + name must still fit IFNAMSIZ.
	long := epairName("averylongstackname", "", 0, 2)
	if len(long) > ifaceMax || !strings.HasSuffix(long, "2") {
		t.Errorf("long name not trimmed around its suffix: %q", long)
	}
}

// A long stack id starting with a digit used to collapse to one name for
// every network: "j" was prepended after trimming, which pushed it back over
// the limit, and the re-trim ate the suffix that made them distinct.
func TestEpairNameNoCollisionOnLongNumericID(t *testing.T) {
	seen := map[string]int{}
	for i := 0; i < 3; i++ {
		seen[epairName("101sonarrxyz", "", 0, i)]++
	}
	if len(seen) != 3 {
		t.Fatalf("names collided: %v", seen)
	}
	for name := range seen {
		if len(name) > ifaceMax {
			t.Errorf("%q is longer than IFNAMSIZ allows (%d)", name, ifaceMax)
		}
		if name[0] >= '0' && name[0] <= '9' {
			t.Errorf("%q starts with a digit", name)
		}
	}
}

// A host-networked bundle carries alias and ip4_inherit in its director
// options. appjail refuses both next to a network -- `alias` is declared
// exclusive with bridge/vnet, and `virtualnet` errors on ip4_inherit by name --
// so leaving them in place turned attaching a network into an exclusivity
// error rather than a jail on that network.
func TestSetDirectorNetworksDropsHostOptions(t *testing.T) {
	seedNetwork(t)
	const hostDirector = `options:
  - alias:
  - ip4_inherit:
  - ip6_inherit:
services:
  web:
    name: immich_web
    options:
      - from: ghcr.io/daemonless/immich-server:latest
`
	out, err := setDirectorNetworks(context.Background(), hostDirector, "immich", []composepkg.Attachment{{Network: "vlan5", IP: "192.168.5.40"}})
	if err != nil {
		t.Fatalf("setDirectorNetworks: %v", err)
	}
	for _, gone := range []string{"alias:", "ip4_inherit", "ip6_inherit"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived the attach -- appjail will refuse it:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "bridge: 'epair:immich bridge:vlan5bridge'") {
		t.Errorf("the network was not attached:\n%s", out)
	}
	// The bundle's own content is still the bundle's.
	if !strings.Contains(out, "immich_web") {
		t.Errorf("lost the service:\n%s", out)
	}
}

// Every jail needs its own epair. A project-level `epair:<stack>` works only
// while the project has one jail: with four, the first takes sb_<stack> into
// its vnet and the rest fail to start with "interface sb_<stack> does not
// exist" -- which is exactly how immich broke when it was put on a bridge.
func TestSetDirectorNetworksPerService(t *testing.T) {
	seedNetwork(t)
	const multi = `options:
  - alias:
  - ip4_inherit:
services:
  server:
    name: immich_server
    options:
      - from: ghcr.io/daemonless/immich-server:latest
  database:
    name: immich_database
    options:
      - from: ghcr.io/daemonless/immich-postgres:latest
`
	out, err := setDirectorNetworks(context.Background(), multi, "immich", []composepkg.Attachment{{Network: "vlan6"}})
	if err != nil {
		t.Fatalf("setDirectorNetworks: %v", err)
	}
	// One epair per service, and no two the same.
	ifaces := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if _, rest, ok := strings.Cut(line, "epair:"); ok {
			name, _, _ := strings.Cut(rest, " ")
			if ifaces[name] {
				t.Errorf("two services share epair %q:\n%s", name, out)
			}
			ifaces[name] = true
		}
	}
	if len(ifaces) != 2 {
		t.Errorf("want one epair per service, got %d:\n%s", len(ifaces), out)
	}
	// Every name has to fit IFNAMSIZ once appjail prefixes the jail side.
	for name := range ifaces {
		if len("sb_"+name) > 15 {
			t.Errorf("sb_%s exceeds IFNAMSIZ", name)
		}
	}
	// Nothing networking-related left at project level: appjail applies a
	// project option to every jail, which overrides what the services declare.
	head, _, _ := strings.Cut(out, "services:")
	for _, gone := range []string{"alias", "ip4_inherit", "bridge:", "virtualnet"} {
		if strings.Contains(head, gone) {
			t.Errorf("%q survived at project level:\n%s", gone, head)
		}
	}
	// And the bundle's own per-service options are untouched.
	if strings.Count(out, "from: ghcr.io/daemonless/") != 2 {
		t.Errorf("lost a service's from::\n%s", out)
	}
}

// An address names one interface. With several services only the first can
// hold it, and the rest need allocation they can ask for -- which a static or
// range network has not got.
func TestSetDirectorNetworksPinNeedsOneService(t *testing.T) {
	seedNetwork(t)
	const multi = `services:
  a:
    name: s_a
    options: []
  b:
    name: s_b
    options: []
`
	_, err := setDirectorNetworks(context.Background(), multi, "s", []composepkg.Attachment{{Network: "vlan5", IP: "192.168.5.9"}})
	if err == nil {
		t.Fatal("accepted one address for two jails")
	}
	if !strings.Contains(err.Error(), "address of its own") {
		t.Errorf("error should say why: %v", err)
	}
}

// immich's immich-server and immich-machine-learning differ only past the
// twelfth character, which is where the name has to be cut to fit IFNAMSIZ.
// Both trimmed to "immichimmich": the first jail took sb_immichimmich into its
// vnet and the second failed to start with "interface sb_immichimmich does not
// exist" -- the per-project epair bug again, one level down.
func TestEpairNamePerServiceNoCollision(t *testing.T) {
	services := []string{"immich-server", "immich-machine-learning", "redis", "database"}
	seen := map[string]string{}
	for i, svc := range services {
		for n := 0; n < 2; n++ {
			name := epairName("immich", svc, i, n)
			if len(name) > ifaceMax {
				t.Errorf("%q is longer than IFNAMSIZ allows (%d)", name, ifaceMax)
			}
			key := svc + "/" + strconv.Itoa(n)
			if prev, dup := seen[name]; dup {
				t.Errorf("%s and %s both got %q", prev, key, name)
			}
			seen[name] = key
		}
	}
}

// A one-service stack keeps the name it already has on disk and in its jail:
// the service index is only added when there is something to tell apart.
func TestEpairNameSingleServiceUnchanged(t *testing.T) {
	if got := epairName("zensical", "", 0, 0); got != "zensical" {
		t.Errorf("single-service epair changed name to %q", got)
	}
}
