package compose

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The caddy compose from the dogfood session -- single service, a comment, and
// host port publishing that collides on a busy host.
const caddyCompose = `services:
  caddy:
    image: "ghcr.io/daemonless/caddy:latest"
    container_name: caddy
    environment:
      - TZ=UTC  # Timezone for the container
    volumes:
      - "/containers/caddy:/config"
    ports:
      - "80:80"
    restart: unless-stopped
`

// mustParse fails the test if the string isn't valid YAML.
func mustParse(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("result is not valid YAML: %v\n%s", err, s)
	}
	return m
}

func TestInjectNetworkStaticIP(t *testing.T) {
	out, err := InjectNetwork(caddyCompose, "vlan5", "192.168.5.50", "")
	if err != nil {
		t.Fatalf("InjectNetwork: %v", err)
	}
	m := mustParse(t, out)

	// Top-level networks: {vlan5: {external: true}}
	nets, _ := m["networks"].(map[string]any)
	v5, _ := nets["vlan5"].(map[string]any)
	if v5["external"] != true {
		t.Fatalf("top-level networks.vlan5.external not true: %+v", m["networks"])
	}

	// Service attached with the static IP.
	svc := m["services"].(map[string]any)["caddy"].(map[string]any)
	snet := svc["networks"].(map[string]any)["vlan5"].(map[string]any)
	if snet["ipv4_address"] != "192.168.5.50" {
		t.Fatalf("service ipv4_address not set: %+v", svc["networks"])
	}

	// Comment must survive the round-trip.
	if !strings.Contains(out, "# Timezone for the container") {
		t.Fatalf("comment lost in round-trip:\n%s", out)
	}
}

func TestInjectNetworkAutoAssign(t *testing.T) {
	out, err := InjectNetwork(caddyCompose, "vlan5", "", "")
	if err != nil {
		t.Fatalf("InjectNetwork: %v", err)
	}
	m := mustParse(t, out)
	svc := m["services"].(map[string]any)["caddy"].(map[string]any)
	list, ok := svc["networks"].([]any)
	if !ok || len(list) != 1 || list[0] != "vlan5" {
		t.Fatalf("auto-assign should use list form [vlan5], got: %+v", svc["networks"])
	}
}

func TestInjectNetworkIPRequiresSingleService(t *testing.T) {
	multi := `services:
  a:
    image: x
  b:
    image: y
`
	if _, err := InjectNetwork(multi, "vlan5", "192.168.5.50", ""); err == nil {
		t.Fatal("expected error assigning a shared IP across two services")
	}
	// Auto-assign across multiple services is fine.
	if _, err := InjectNetwork(multi, "vlan5", "", ""); err != nil {
		t.Fatalf("auto-assign multi-service should succeed: %v", err)
	}
}

// Attaching has to be repeatable: the Resources tab sends the whole thing
// again to change an address or a MAC, and that arrives with the service
// already on a network.
func TestInjectNetworkReattaches(t *testing.T) {
	withNet := `services:
  a:
    image: x
    networks:
      - other
`
	out, err := InjectNetwork(withNet, "vlan5", "", "02:1a:2b:3c:4d:5e")
	if err != nil {
		t.Fatalf("re-attach refused: %v", err)
	}
	if net, _ := AttachedNetwork(out); net != "vlan5" {
		t.Errorf("network not replaced, got %q:\n%s", net, out)
	}
	if AttachedMAC(out) != "02:1a:2b:3c:4d:5e" {
		t.Errorf("MAC not set:\n%s", out)
	}
	// Clearing the field drops the pin rather than leaving a stale one.
	back, err := InjectNetwork(out, "vlan5", "", "")
	if err != nil {
		t.Fatalf("re-attach without a MAC refused: %v", err)
	}
	if AttachedMAC(back) != "" {
		t.Errorf("stale MAC kept:\n%s", back)
	}
}

// Several networks is a stack fjord can now describe, so the list it is given
// replaces whatever was there -- the caller seeds its table from
// AttachedNetworks, so what gets written is what the user was shown.
func TestInjectNetworksReplacesTheWholeSet(t *testing.T) {
	withNets := `services:
  a:
    image: x
    networks:
      - other
      - second
`
	out, err := InjectNetworks(withNets, []Attachment{
		{Network: "lan", MAC: "02:1a:2b:3c:4d:5e"},
		{Network: "private", IP: "10.100.0.5"},
	})
	if err != nil {
		t.Fatalf("replace refused: %v", err)
	}
	got := AttachedNetworks(out)
	if len(got) != 2 || got[0].Network != "lan" || got[1].Network != "private" {
		t.Fatalf("network set not replaced, got %+v:\n%s", got, out)
	}
	// Order is the interface order, and each keeps its own pin.
	if got[0].MAC != "02:1a:2b:3c:4d:5e" || got[1].IP != "10.100.0.5" {
		t.Errorf("pins not written per network: %+v\n%s", got, out)
	}
	if got[0].IP != "" || got[1].MAC != "" {
		t.Errorf("pins leaked between networks: %+v", got)
	}
}

// A network listed twice would be two interfaces fjord cannot tell apart.
func TestInjectNetworksRejectsDuplicates(t *testing.T) {
	in := "services:\n  a:\n    image: x\n"
	if _, err := InjectNetworks(in, []Attachment{{Network: "lan"}, {Network: "lan"}}); err == nil {
		t.Fatal("expected error when a network is listed twice")
	}
}

func TestSplitPortSpecAndParsePort(t *testing.T) {
	cases := map[string][2]string{
		"80":                {"80", "80"},
		"8080:80":           {"8080", "80"},
		"127.0.0.1:8080:80": {"8080", "80"},
		"[::1]:8443:443":    {"8443", "443"},
		"0.0.0.0:53:53":     {"53", "53"},
	}
	for in, want := range cases {
		h, c := SplitPortSpec(in)
		if h != want[0] || c != want[1] {
			t.Errorf("%q: got %s,%s want %s,%s", in, h, c, want[0], want[1])
		}
	}
	pm, ok := parsePort("127.0.0.1:5432:5432/tcp")
	if !ok || pm.Host != 5432 || pm.Container != 5432 || pm.Proto != "tcp" {
		t.Fatalf("ip-prefixed parsePort: %+v ok=%v", pm, ok)
	}
	ports := PublishedPorts("services:\n  db:\n    ports:\n      - \"127.0.0.1:5432:5432\"\n      - \"8080:80/udp\"\n", nil)
	if len(ports) != 2 || ports[0].Host != 5432 || ports[1].Proto != "udp" {
		t.Fatalf("PublishedPorts: %+v", ports)
	}
}

func TestDropTopLevelKey(t *testing.T) {
	in := "name: custom\nservices:\n  a:\n    image: x\n"
	out := DropTopLevelKey(in, "name")
	if strings.Contains(out, "name: custom") || !strings.Contains(out, "image: x") {
		t.Fatalf("got %q", out)
	}
	if DropTopLevelKey(in, "absent") != in {
		t.Fatal("absent key must leave input unchanged")
	}
}

// Stacks attached before per-network pins carry one service-level
// mac_address; podman-compose applies it to the first network, so reading it
// any other way would lose the pin on the next save.
func TestAttachedNetworksReadsLegacyServiceMAC(t *testing.T) {
	in := `services:
  app:
    image: x
    mac_address: 02:aa:bb:cc:dd:ee
    networks:
      - lan
`
	got := AttachedNetworks(in)
	if len(got) != 1 || got[0].MAC != "02:aa:bb:cc:dd:ee" {
		t.Fatalf("legacy service-level MAC not read: %+v", got)
	}
	// Writing it back moves it onto the network it belongs to.
	out, err := InjectNetworks(in, got)
	if err != nil {
		t.Fatalf("re-inject: %v", err)
	}
	if !strings.Contains(out, "mac_address: 02:aa:bb:cc:dd:ee") || strings.Contains(out, "\n    mac_address:") {
		t.Errorf("MAC not moved under the network:\n%s", out)
	}
}

// Detaching has to undo what attaching did, or a stack that came off its
// network has no published ports and cannot be reached at all.
func TestDetachNetworksRestoresPorts(t *testing.T) {
	in := "services:\n  app:\n    image: x\n    ports:\n      - \"8080:80\"\n"
	attached, err := InjectNetworks(in, []Attachment{{Network: "lan", MAC: "02:1a:2b:3c:4d:5e"}})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if strings.Contains(attached, "\n    ports:") {
		t.Fatalf("attach should have stashed the ports:\n%s", attached)
	}
	out, err := DetachNetworks(attached)
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if !strings.Contains(out, `"8080:80"`) || strings.Contains(out, "x-fjord-published") {
		t.Errorf("ports not restored:\n%s", out)
	}
	for _, gone := range []string{"\n    networks:", "\n    mac_address", "extra_hosts", "external"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived the detach:\n%s", gone, out)
		}
	}
	if len(AttachedNetworks(out)) != 0 {
		t.Errorf("still reported as attached:\n%s", out)
	}
	// Detached, but not forgotten: the addresses are kept so going back does
	// not mean typing them again. Matched on the service-level mac_address
	// above (a two-space indent), since the stash holds one too.
	stash := StashedNetworks(out)
	if len(stash) != 1 || stash[0].Network != "lan" || stash[0].MAC != "02:1a:2b:3c:4d:5e" {
		t.Errorf("attachment not stashed for a trip back: %+v\n%s", stash, out)
	}
}

// The chain the user hit: three networks, off to a mode, through another
// mode, and back. The stash has to survive the middle transition -- an
// unguarded stash would overwrite the real one with the empty set it finds
// there -- and has to be gone once the attachments are written again.
func TestStashSurvivesModeChain(t *testing.T) {
	in := "services:\n  app:\n    image: x\n    ports:\n      - \"8080:80\"\n"
	attached, err := InjectNetworks(in, []Attachment{
		{Network: "lan", IP: "192.168.4.10", MAC: "02:1a:2b:3c:4d:5e"},
		{Network: "lan2"},
		{Network: "tttt"},
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	cur := attached
	for _, step := range []func(string) (string, error){DisableNetwork, HostNetwork, DetachNetworks, DisableNetwork} {
		if cur, err = step(cur); err != nil {
			t.Fatalf("transition: %v", err)
		}
		got := StashedNetworks(cur)
		if len(got) != 3 || got[0].Network != "lan" || got[1].Network != "lan2" || got[2].Network != "tttt" {
			t.Fatalf("stash lost through a transition: %+v\n%s", got, cur)
		}
		if got[0].IP != "192.168.4.10" || got[0].MAC != "02:1a:2b:3c:4d:5e" {
			t.Fatalf("pins lost through a transition: %+v\n%s", got[0], cur)
		}
		// Stashed on a mode, handed back on bridge -- present either way.
		if !strings.Contains(cur, `"8080:80"`) {
			t.Fatalf("published ports lost through a transition:\n%s", cur)
		}
	}
	// Restoring is the UI handing the stash straight back.
	back, err := InjectNetworks(cur, StashedNetworks(cur))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if strings.Contains(back, "x-fjord-networks") {
		t.Errorf("stash outlived the attachment that consumed it:\n%s", back)
	}
	got := AttachedNetworks(back)
	if len(got) != 3 || got[0].IP != "192.168.4.10" || got[0].MAC != "02:1a:2b:3c:4d:5e" {
		t.Errorf("restore did not put the addresses back: %+v\n%s", got, back)
	}
}

// "none" on FreeBSD needs the vnet annotation as well: network_mode alone
// leaves the jail sharing the host's stack, which is the opposite of off.
func TestDisableNetwork(t *testing.T) {
	in := "services:\n  app:\n    image: x\n    ports:\n      - \"8080:80\"\n"
	out, err := DisableNetwork(in)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	for _, want := range []string{"network_mode: none", "org.freebsd.jail.vnet: new"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\n    ports:") {
		t.Errorf("published ports should be stashed, not left to bind nothing:\n%s", out)
	}
	if NetworkMode(out) != None {
		t.Errorf("not reported as none, got %q:\n%s", NetworkMode(out), out)
	}
	// Attaching a network afterwards is the way back, and must undo it.
	back, err := InjectNetworks(out, []Attachment{{Network: "lan"}})
	if err != nil {
		t.Fatalf("re-attach after disable: %v", err)
	}
	if NetworkMode(back) != "" || strings.Contains(back, "network_mode") {
		t.Errorf("network_mode survived an attach:\n%s", back)
	}
}

// The four ways through the stack's network picker, round-tripped. Each mode
// stashes the published ports, so a user who clicks through them has to get
// the same compose back -- the first cut of DetachNetworks left network_mode
// in place, which made "back to bridge" a silent no-op.
func TestNetworkModeRoundTrip(t *testing.T) {
	in := "services:\n  app:\n    image: x\n    ports:\n      - \"8080:80\"\n"
	for _, tc := range []struct {
		mode string
		set  func(string) (string, error)
	}{
		{None, DisableNetwork},
		{Host, HostNetwork},
	} {
		out, err := tc.set(in)
		if err != nil {
			t.Fatalf("%s: %v", tc.mode, err)
		}
		if got := NetworkMode(out); got != tc.mode {
			t.Errorf("%s: read back as %q:\n%s", tc.mode, got, out)
		}
		// host shares the host's stack, so an empty vnet of its own is the
		// one thing it must NOT have.
		if hasVnet := strings.Contains(out, "org.freebsd.jail.vnet"); hasVnet != (tc.mode == None) {
			t.Errorf("%s: vnet annotation = %v:\n%s", tc.mode, hasVnet, out)
		}
		back, err := DetachNetworks(out)
		if err != nil {
			t.Fatalf("%s -> bridge: %v", tc.mode, err)
		}
		// Asserted as properties rather than against `in`: DetachNetworks
		// re-encodes at its own indent, so byte equality would be testing the
		// fixture's formatting rather than the code.
		if !strings.Contains(back, "ports:") || strings.Contains(back, "x-fjord-published") {
			t.Errorf("%s -> bridge did not give the published ports back:\n%s", tc.mode, back)
		}
		for _, leftover := range []string{"network_mode", "org.freebsd.jail.vnet", "annotations"} {
			if strings.Contains(back, leftover) {
				t.Errorf("%s -> bridge left %q behind:\n%s", tc.mode, leftover, back)
			}
		}
	}
	// And straight from one mode to the other, without passing through bridge.
	h, err := HostNetwork(mustDisable(t, in))
	if err != nil {
		t.Fatalf("none -> host: %v", err)
	}
	if strings.Contains(h, "org.freebsd.jail.vnet") {
		t.Errorf("none -> host kept the vnet annotation:\n%s", h)
	}
}

func mustDisable(t *testing.T, in string) string {
	t.Helper()
	out, err := DisableNetwork(in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
