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

// A stack whose app is on the LAN and whose database and cache are on a
// private segment. Before this, four services sharing one network read as the
// same network four times -- "network \"lan\" is listed twice" -- and nothing
// saved.
func TestInjectNetworksPerService(t *testing.T) {
	const in = `services:
  web:
    image: nginx
    ports:
      - "8080:80"
  db:
    image: postgres
  cache:
    image: redis
`
	out, err := InjectNetworks(in, []Attachment{
		{Network: "lan", Service: "web", IP: "192.168.4.90"},
		{Network: "priv", Service: "web", IP: "10.99.0.5"},
		{Network: "priv", Service: "db", IP: "10.99.0.2"},
		{Network: "priv", Service: "cache", IP: "10.99.0.3"},
	})
	if err != nil {
		t.Fatalf("InjectNetworks: %v", err)
	}
	byService := ServiceAttachments(out)
	if n := byService["web"]; len(n) != 2 {
		t.Errorf("web has %d networks, want 2: %+v", len(n), n)
	}
	for _, svc := range []string{"db", "cache"} {
		n := byService[svc]
		if len(n) != 1 || n[0].Network != "priv" {
			t.Errorf("%s = %+v, want one attachment to priv", svc, n)
		}
	}
	// Every service keeps its OWN address: the one-pin rule exists only
	// because a single list used to be shared by every service.
	for svc, want := range map[string]string{"web": "192.168.4.90", "db": "10.99.0.2", "cache": "10.99.0.3"} {
		found := false
		for _, a := range byService[svc] {
			if a.IP == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s did not keep %s: %+v", svc, want, byService[svc])
		}
	}
}

// A service no attachment names keeps what it had, rather than being detached
// by a payload that never mentioned it.
func TestInjectNetworksPerServiceLeavesOthersAlone(t *testing.T) {
	const in = `services:
  web:
    image: nginx
  db:
    image: postgres
    network_mode: host
`
	out, err := InjectNetworks(in, []Attachment{{Network: "lan", Service: "web"}})
	if err != nil {
		t.Fatalf("InjectNetworks: %v", err)
	}
	if got := ServiceAttachments(out); len(got["db"]) != 0 {
		t.Errorf("db was attached without being asked: %+v", got["db"])
	}
	if !strings.Contains(out, "network_mode: host") {
		t.Errorf("db lost its mode:\n%s", out)
	}
}

// The same service on one network twice is still two names for one interface.
func TestInjectNetworksPerServiceRejectsDuplicate(t *testing.T) {
	const in = "services:\n  web:\n    image: nginx\n"
	_, err := InjectNetworks(in, []Attachment{
		{Network: "lan", Service: "web"},
		{Network: "lan", Service: "web"},
	})
	if err == nil {
		t.Fatal("accepted one service on the same network twice")
	}
}

// Each service gets its own mode; the rest of the stack is untouched.
func TestSetServiceModes(t *testing.T) {
	const in = `services:
  web:
    image: nginx
    networks: [lan]
    mac_address: 02:1a:2b:3c:4d:5e
  db:
    image: postgres
    mac_address: 02:1a:2b:3c:4d:5f
  cache:
    image: redis
`
	out, err := SetServiceModes(in, map[string]string{"web": "host", "db": "none", "cache": "bridge"})
	if err != nil {
		t.Fatalf("SetServiceModes: %v", err)
	}
	if !strings.Contains(out, "network_mode: host") || !strings.Contains(out, "network_mode: none") {
		t.Errorf("modes not written:\n%s", out)
	}
	if !strings.Contains(out, "org.freebsd.jail.vnet: new") {
		t.Errorf("none mode did not add org.freebsd.jail.vnet: new:\n%s", out)
	}
	if strings.Contains(out, "02:1a:2b:3c:4d:5f") {
		t.Errorf("db mac_address survived SetServiceModes:\n%s", out)
	}
	if !strings.Contains(out, "x-fjord-networks:") || !strings.Contains(out, "02:1a:2b:3c:4d:5e") {
		t.Errorf("web mac_address was not stashed into x-fjord-networks:\n%s", out)
	}
	// bridge is the engine's default, which in compose is the absence of both.
	if strings.Count(out, "network_mode:") != 2 {
		t.Errorf("bridge should write no mode:\n%s", out)
	}
	// A mode and a network are exclusive, so the attachment goes.
	if len(ServiceAttachments(out)["web"]) != 0 {
		t.Errorf("web kept its network alongside a mode:\n%s", out)
	}
}

// A host-networked app has no ports entry; SeedPublished notes its web
// port, and a move to the bridge publishes it. On the host it stays as it
// ships, and an app that publishes already is left alone.
func TestSeededPortComesBackOnTheBridge(t *testing.T) {
	const in = `services:
  actual:
    image: ghcr.io/daemonless/actual:latest
    network_mode: host
`
	seeded, err := SeedPublished(in, "actual", "5006")
	if err != nil || !strings.Contains(seeded, "x-fjord-published:\n      - 5006:5006") || strings.Contains(seeded, "ports:") {
		t.Fatalf("seed: %v\n%s", err, seeded)
	}
	bridged, err := SetServiceModes(seeded, map[string]string{"actual": "bridge"})
	if err != nil || !strings.Contains(bridged, "ports:\n      - 5006:5006") || strings.Contains(bridged, "network_mode") || strings.Contains(bridged, "x-fjord-published") {
		t.Fatalf("bridge: %v\n%s", err, bridged)
	}
	back, err := SetServiceModes(bridged, map[string]string{"actual": "host"})
	if err != nil || !strings.Contains(back, "network_mode: host") || strings.Contains(back, "ports:") || !strings.Contains(back, "x-fjord-published") {
		t.Fatalf("host again: %v\n%s", err, back)
	}
	same, _ := SeedPublished("services:\n  web:\n    ports:\n      - 8080:80\n", "web", "80")
	if strings.Contains(same, "x-fjord-published") {
		t.Errorf("an app that publishes was seeded:\n%s", same)
	}
}

// Attaching stashes a service's ports; a private segment is the case where
// they have to come back, because the address it hands out reaches no browser.
func TestRepublishPorts(t *testing.T) {
	in := `services:
  web:
    image: x
    ports:
      - "2283:2283"
  db:
    image: y
    ports:
      - "5432:5432"
`
	attached, err := InjectNetworks(in, []Attachment{
		{Network: "priv", Service: "web"},
		{Network: "priv", Service: "db"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(attached, "\n    ports:") {
		t.Fatalf("attaching should stash the ports:\n%s", attached)
	}
	out, err := RepublishPorts(attached, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	web := serviceBlock(out, "web")
	if !strings.Contains(web, "ports:") || !strings.Contains(web, `"2283:2283"`) {
		t.Errorf("web did not get its ports back:\n%s", out)
	}
	if strings.Contains(web, "x-fjord-published") {
		t.Errorf("web's stash should be gone once it is given back:\n%s", out)
	}
	// Only the services named: db was not asked for, so its ports stay stashed.
	if db := serviceBlock(out, "db"); strings.Contains(db, "\n    ports:") {
		t.Errorf("db's ports were given back without being asked for:\n%s", out)
	}
	// And a later move onto a real segment stashes them again, so the two
	// halves of this are symmetric rather than a one-way door.
	again, err := InjectNetworks(out, []Attachment{{Network: "lan", Service: "web"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(again, "\n    ports:") {
		t.Errorf("moving web onto a real network should stash its ports again:\n%s", again)
	}
}

// serviceBlock is one service's YAML out of a rendered compose, so an
// assertion about one service cannot be satisfied by another's lines.
func serviceBlock(composeYAML, name string) string {
	start := strings.Index(composeYAML, "\n  "+name+":\n")
	if start < 0 {
		return ""
	}
	rest := composeYAML[start+1:]
	for i := 1; i < len(rest); i++ {
		if rest[i] == '\n' && i+3 < len(rest) && rest[i+1] == ' ' && rest[i+2] == ' ' && rest[i+3] != ' ' {
			return rest[:i]
		}
		if rest[i] == '\n' && i+1 < len(rest) && rest[i+1] != ' ' {
			return rest[:i]
		}
	}
	return rest
}

// A dual-stack attachment carries both families, and both have to survive the
// round trip: writing ipv6_address but never reading it back meant the next
// save dropped the address the user had just pinned.
func TestInjectNetworksDualStackRoundTrip(t *testing.T) {
	const base = `services:
  app:
    image: nginx
`
	out, err := InjectNetworks(base, []Attachment{{Network: "lan", IP: "192.168.4.10", IP6: "fd00:4:103::10"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ipv4_address: 192.168.4.10", "ipv6_address: fd00:4:103::10"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	got := AttachedNetworks(out)
	if len(got) != 1 {
		t.Fatalf("want 1 attachment, got %d: %+v", len(got), got)
	}
	if got[0].IP != "192.168.4.10" || got[0].IP6 != "fd00:4:103::10" {
		t.Errorf("round trip lost an address: %+v", got[0])
	}
}

// A v6-only attachment is pinned too. Keying "pinned" off the v4 address alone
// would send it down the unpinned path and write no address at all.
func TestInjectNetworksIPv6Only(t *testing.T) {
	const base = `services:
  app:
    image: nginx
`
	out, err := InjectNetworks(base, []Attachment{{Network: "lan", IP6: "fd00:4:103::10"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ipv6_address: fd00:4:103::10") {
		t.Fatalf("v6-only attachment wrote no address:\n%s", out)
	}
	if strings.Contains(out, "ipv4_address") {
		t.Errorf("invented an IPv4 address:\n%s", out)
	}
	got := AttachedNetworks(out)
	if len(got) != 1 || got[0].IP6 != "fd00:4:103::10" || got[0].IP != "" {
		t.Errorf("round trip: %+v", got)
	}
}

// The built-in bridge next to a LAN network: written as compose's "default"
// (declared, not external -- podman-compose refuses it undeclared) and read
// back as "bridge". Proven on netlab: both interfaces up, the published port
// and the LAN address both answer.
func TestInjectNetworksBridgeWithOthers(t *testing.T) {
	out, err := InjectNetworks(caddyCompose, []Attachment{
		{Network: Bridge, Service: "caddy"},
		{Network: "lan-dhcp", Service: "caddy", MAC: "58:9c:fc:10:b7:36"},
	})
	if err != nil {
		t.Fatalf("InjectNetworks: %v", err)
	}
	m := mustParse(t, out)
	top := m["networks"].(map[string]any)
	if d, ok := top["default"]; !ok || d == nil || len(d.(map[string]any)) != 0 {
		t.Errorf("top-level default: got %#v, want an empty mapping\n%s", top["default"], out)
	}
	if _, ok := top["bridge"]; ok {
		t.Errorf("wrote a network named bridge:\n%s", out)
	}
	if ext := top["lan-dhcp"].(map[string]any)["external"]; ext != true {
		t.Errorf("lan-dhcp not external:\n%s", out)
	}
	svc := m["services"].(map[string]any)["caddy"].(map[string]any)
	nets := svc["networks"].(map[string]any)
	if _, ok := nets["default"]; !ok {
		t.Errorf("service networks lack default:\n%s", out)
	}
	got := ServiceAttachments(out)["caddy"]
	if len(got) != 2 || got[0].Network != Bridge || got[1].Network != "lan-dhcp" || got[1].MAC != "58:9c:fc:10:b7:36" {
		t.Errorf("read back %+v, want bridge then lan-dhcp with its MAC", got)
	}
}

// A network the compose declares itself is the stack's own: compose makes it
// under the project's name when the stack starts, so the host's list never
// has it (an imported mealie showed "mealie_internal (not on this host)").
// An external one, and compose's "default", are not.
func TestAttachmentsMarkDeclaredNetworksOwn(t *testing.T) {
	const in = `services:
  app:
    image: mealie
    networks:
      - lan
      - mealie_internal
      - default
  db:
    image: postgres
    networks:
      - lan
      - mealie_internal
      - default
networks:
  lan:
    external: true
  mealie_internal:
    driver: bridge
  default: {}
`
	want := map[string]bool{"lan": false, "mealie_internal": true, "bridge": false}
	check := func(what string, atts []Attachment) {
		if len(atts) != 3 {
			t.Fatalf("%s: %+v", what, atts)
		}
		for _, a := range atts {
			if w, ok := want[a.Network]; !ok || a.Own != w {
				t.Errorf("%s: %s own = %v, want %v", what, a.Network, a.Own, w)
			}
		}
	}
	check("AttachedNetworks", AttachedNetworks(in))
	check("ServiceAttachments app", ServiceAttachments(in)["app"])
	check("ServiceAttachments db", ServiceAttachments(in)["db"])
	// Saving the rows back leaves the declared network as it was.
	out, err := InjectNetworks(in, []Attachment{{Network: "lan", Service: "app"}, {Network: "mealie_internal", Service: "app", Own: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "mealie_internal:\n    driver: bridge") {
		t.Fatalf("declared network rewritten:\n%s", out)
	}
}

func TestInjectAndDetachPreserveServiceAliases(t *testing.T) {
	const in = `services:
  db:
    image: postgres:16
    networks:
      default:
        aliases:
          - database
          - pg
  app:
    image: myapp
    ports:
      - "8080:8080"
`
	// Attach stack to lan: db's default aliases must NOT be copied onto external lan,
	// and instead are stashed so DetachNetworks can restore them on default.
	onLan, err := InjectNetworks(in, []Attachment{{Network: "lan", IP: "10.0.1.50"}})
	if err != nil {
		t.Fatalf("InjectNetworks: %v", err)
	}
	m := mustParse(t, onLan)
	dbNets, ok := m["services"].(map[string]any)["db"].(map[string]any)["networks"].([]any)
	if !ok || len(dbNets) != 1 || dbNets[0] != "lan" {
		t.Fatalf("db.networks on lan = %#v, want plain [lan] (aliases not copied to shared lan):\n%s", m["services"].(map[string]any)["db"].(map[string]any)["networks"], onLan)
	}

	// Re-attaching with a different network or IP keeps the stashed default aliases.
	onVlan, err := InjectNetworks(onLan, []Attachment{{Network: "vlan20", IP: "10.0.20.50"}})
	if err != nil {
		t.Fatalf("InjectNetworks(vlan20): %v", err)
	}

	// DetachNetworks restores db's original default network with aliases intact,
	// and declares default at top-level so podman-compose accepts it.
	detached, err := DetachNetworks(onVlan)
	if err != nil {
		t.Fatalf("DetachNetworks: %v", err)
	}
	m3 := mustParse(t, detached)
	defCfg := m3["services"].(map[string]any)["db"].(map[string]any)["networks"].(map[string]any)["default"].(map[string]any)
	if got, ok := defCfg["aliases"].([]any); !ok || len(got) != 2 || got[0] != "database" || got[1] != "pg" {
		t.Fatalf("restored db.networks.default.aliases = %#v, want [database pg]:\n%s", defCfg["aliases"], detached)
	}
	topNets, ok := m3["networks"].(map[string]any)
	if !ok || topNets["default"] == nil {
		t.Fatalf("expected top-level networks.default declaration when service uses default:\n%s", detached)
	}
}

func TestInjectAndDetachPreserveOwnDeclaredNetworks(t *testing.T) {
	const in = `services:
  app:
    image: immich-server
    ports:
      - "2283:2283"
    networks:
      - backend
  db:
    image: postgres:16
    networks:
      backend:
        aliases:
          - database
networks:
  backend:
    internal: true
`
	// Attaching stack to "lan" keeps "backend" attached on both services alongside "lan".
	onLan, err := InjectNetworks(in, []Attachment{{Network: "lan", IP: "10.0.1.60"}})
	if err != nil {
		t.Fatalf("InjectNetworks: %v", err)
	}
	sa := ServiceAttachments(onLan)
	for _, svc := range []string{"app", "db"} {
		atts := sa[svc]
		var hasLan, hasBackend bool
		for _, a := range atts {
			if a.Network == "lan" && !a.Own {
				hasLan = true
			}
			if a.Network == "backend" && a.Own {
				hasBackend = true
			}
		}
		if !hasLan || !hasBackend {
			t.Errorf("%s attachments = %+v, want both lan and own backend:\n%s", svc, atts, onLan)
		}
	}
	// db keeps its database alias on backend.
	m := mustParse(t, onLan)
	dbBackend := m["services"].(map[string]any)["db"].(map[string]any)["networks"].(map[string]any)["backend"].(map[string]any)
	if got, ok := dbBackend["aliases"].([]any); !ok || len(got) != 1 || got[0] != "database" {
		t.Errorf("db.networks.backend.aliases = %#v, want [database]:\n%s", dbBackend["aliases"], onLan)
	}
	// Re-attaching stack with a new IP on lan does not lose the stashed ports or backend network.
	rePinned, err := InjectNetworks(onLan, []Attachment{{Network: "lan", IP: "10.0.1.61"}})
	if err != nil {
		t.Fatalf("re-pin InjectNetworks: %v", err)
	}
	if got := ServiceAttachments(rePinned)["app"]; len(got) != 2 || got[0].IP != "10.0.1.61" {
		t.Errorf("re-pinned app attachments = %+v, want IP 10.0.1.61 and backend:\n%s", got, rePinned)
	}

	// DetachNetworks strips "lan" and restores "ports", while keeping stack-owned "backend".
	detached, err := DetachNetworks(rePinned)
	if err != nil {
		t.Fatalf("DetachNetworks: %v", err)
	}
	if !strings.Contains(detached, "2283:2283") {
		t.Errorf("DetachNetworks did not restore ports:\n%s", detached)
	}
	if !strings.Contains(detached, "internal: true") {
		t.Errorf("DetachNetworks lost top-level backend network:\n%s", detached)
	}
	for _, svc := range []string{"app", "db"} {
		atts := ServiceAttachments(detached)[svc]
		if len(atts) != 1 || atts[0].Network != "backend" || !atts[0].Own {
			t.Errorf("after DetachNetworks, %s attachments = %+v, want [backend (own)]:\n%s", svc, atts, detached)
		}
	}
}

func TestRestoreAndStashPortsMergeLiveAndStashed(t *testing.T) {
	// Service already has stashed ports in x-fjord-published AND
	// the user added a live ports: entry while on a LAN network.
	const withLiveAndStash = `services:
  caddy:
    image: caddy
    ports:
      - "8443:8443"
    x-fjord-published:
      - "80:80"
      - "443:443"
      - "8443:8443"
    networks:
      - lan
networks:
  lan:
    external: true
`
	// Detaching restores stashed ports without discarding the live port (deduplicated).
	detached, err := DetachNetworks(withLiveAndStash)
	if err != nil {
		t.Fatalf("DetachNetworks: %v", err)
	}
	ports := PublishedPorts(detached, nil)
	if len(ports) != 3 || ports[0].Host != 80 || ports[1].Host != 443 || ports[2].Host != 8443 {
		t.Fatalf("PublishedPorts after DetachNetworks = %+v, want [80 443 8443]:\n%s", ports, detached)
	}
	if strings.Contains(detached, "x-fjord-published") {
		t.Errorf("x-fjord-published should be removed after restore:\n%s", detached)
	}

	// Also verify when stashing (e.g. DisableNetwork when both live ports and x-fjord-published exist):
	const beforeStash = `services:
  caddy:
    image: caddy
    ports:
      - "9090:9090"
    x-fjord-published:
      - "80:80"
`
	disabled, err := DisableNetwork(beforeStash)
	if err != nil {
		t.Fatalf("DisableNetwork: %v", err)
	}
	restored, err := DetachNetworks(disabled)
	if err != nil {
		t.Fatalf("DetachNetworks: %v", err)
	}
	gotPorts := PublishedPorts(restored, nil)
	if len(gotPorts) != 2 || gotPorts[0].Host != 80 || gotPorts[1].Host != 9090 {
		t.Fatalf("PublishedPorts after stash+restore = %+v, want [80 9090]:\n%s", gotPorts, restored)
	}
}

func TestPerServiceInjectKeepsUntouchedServiceStashes(t *testing.T) {
	// Service "a" was previously moved to network_mode: host (so its ports and networks
	// were stashed in x-fjord-published and x-fjord-networks). Now we call per-service
	// InjectNetworks touching only service "b". Service "a"'s stashes must not be stripped.
	const twoServices = `services:
  a:
    image: app-a
    ports:
      - "8080:8080"
    networks:
      default:
        aliases:
          - alpha
  b:
    image: app-b
    ports:
      - "9090:9090"
`
	aHost, err := SetServiceModes(twoServices, map[string]string{"a": "host"})
	if err != nil {
		t.Fatalf("SetServiceModes: %v", err)
	}
	if !strings.Contains(aHost, "x-fjord-published") || !strings.Contains(aHost, "x-fjord-networks") {
		t.Fatalf("expected stashes on service a:\n%s", aHost)
	}

	// Attach only service "b" to lan.
	bLan, err := InjectNetworks(aHost, []Attachment{{Service: "b", Network: "lan", IP: "10.0.1.25"}})
	if err != nil {
		t.Fatalf("InjectNetworks(b): %v", err)
	}
	m := mustParse(t, bLan)
	svcA := m["services"].(map[string]any)["a"].(map[string]any)
	if svcA["x-fjord-published"] == nil {
		t.Errorf("untouched service a lost x-fjord-published:\n%s", bLan)
	}
	if svcA["x-fjord-networks"] == nil {
		t.Errorf("untouched service a lost x-fjord-networks:\n%s", bLan)
	}

	// Clearing host mode on "a" now restores its stashed ports and aliases.
	aBridge, err := SetServiceModes(bLan, map[string]string{"a": ""})
	if err != nil {
		t.Fatalf("SetServiceModes(clear a): %v", err)
	}
	m2 := mustParse(t, aBridge)
	svcA2 := m2["services"].(map[string]any)["a"].(map[string]any)
	if svcA2["ports"] == nil {
		t.Errorf("service a did not restore ports after clearing host mode:\n%s", aBridge)
	}
	if svcA2["networks"] == nil {
		t.Errorf("service a did not restore networks after clearing host mode:\n%s", aBridge)
	}
}

func TestMapEnsureBareTopLevelNetworks(t *testing.T) {
	// A compose file with a bare top-level `networks:` key (YAML null scalar)
	// or a bare service `networks:` key must not fail or drop the declaration.
	const bareNetworks = `services:
  app:
    image: nginx
    networks:
networks:
  existing_net:
`
	out, err := InjectNetworks(bareNetworks, []Attachment{{Network: "lan", IP: "10.0.1.99"}})
	if err != nil {
		t.Fatalf("InjectNetworks on bare networks key: %v", err)
	}
	m := mustParse(t, out)
	topNets, ok := m["networks"].(map[string]any)
	if !ok {
		t.Fatalf("top-level networks is not a map:\n%s", out)
	}
	if _, ok := topNets["lan"]; !ok {
		t.Errorf("top-level networks missing lan:\n%s", out)
	}
	if _, ok := topNets["existing_net"]; !ok {
		t.Errorf("top-level networks lost existing_net:\n%s", out)
	}
}

func TestPreserveServiceAndContainerNetworkMode(t *testing.T) {
	const sidecarCompose = `services:
  gluetun:
    image: qmcgaw/gluetun
    ports:
      - "8888:8888"
  app:
    image: curlimages/curl
    network_mode: service:gluetun
  helper:
    image: busybox
    network_mode: container:vpn
`
	readModes := func(raw string) map[string]string {
		m := mustParse(t, raw)
		out := map[string]string{}
		for name, v := range m["services"].(map[string]any) {
			if nm, ok := v.(map[string]any)["network_mode"].(string); ok {
				out[name] = nm
			}
		}
		return out
	}

	// Stack-wide DisableNetwork / HostNetwork / DetachNetworks must not overwrite or delete
	// network_mode: service:gluetun or container:vpn on sidecar services.
	disabled, err := DisableNetwork(sidecarCompose)
	if err != nil {
		t.Fatalf("DisableNetwork: %v", err)
	}
	modes := readModes(disabled)
	if modes["gluetun"] != "none" {
		t.Errorf("gluetun network_mode = %q, want none", modes["gluetun"])
	}
	if modes["app"] != "service:gluetun" {
		t.Errorf("app network_mode = %q, want service:gluetun", modes["app"])
	}
	if modes["helper"] != "container:vpn" {
		t.Errorf("helper network_mode = %q, want container:vpn", modes["helper"])
	}

	reEnabled, err := DetachNetworks(disabled)
	if err != nil {
		t.Fatalf("DetachNetworks: %v", err)
	}
	modesAfter := readModes(reEnabled)
	if modesAfter["gluetun"] != "" {
		t.Errorf("gluetun network_mode after DetachNetworks = %q, want empty", modesAfter["gluetun"])
	}
	if modesAfter["app"] != "service:gluetun" {
		t.Errorf("app network_mode after DetachNetworks = %q, want service:gluetun", modesAfter["app"])
	}
	if modesAfter["helper"] != "container:vpn" {
		t.Errorf("helper network_mode after DetachNetworks = %q, want container:vpn", modesAfter["helper"])
	}

	// SetServiceModes with bridge/empty mode on app must also leave service:gluetun intact.
	cleared, err := SetServiceModes(sidecarCompose, map[string]string{"app": "bridge", "helper": "bridge"})
	if err != nil {
		t.Fatalf("SetServiceModes: %v", err)
	}
	modesCleared := readModes(cleared)
	if modesCleared["app"] != "service:gluetun" || modesCleared["helper"] != "container:vpn" {
		t.Errorf("modes after SetServiceModes = %+v, want service:gluetun and container:vpn preserved", modesCleared)
	}

	// DetachNetworks must also leave service:gluetun and container:vpn intact.
	onLan, err := InjectNetworks(sidecarCompose, []Attachment{{Service: "gluetun", Network: "lan", IP: "10.0.1.77"}})
	if err != nil {
		t.Fatalf("InjectNetworks(gluetun): %v", err)
	}
	detached, err := DetachNetworks(onLan)
	if err != nil {
		t.Fatalf("DetachNetworks: %v", err)
	}
	modesDetached := readModes(detached)
	if modesDetached["app"] != "service:gluetun" || modesDetached["helper"] != "container:vpn" {
		t.Errorf("modes after DetachNetworks = %+v, want service:gluetun and container:vpn preserved", modesDetached)
	}
}

func TestInjectNetworksWithYAMLAnchors(t *testing.T) {
	const anchored = `x-common: &common
  restart: unless-stopped
  ports:
    - "8080:8080"

services:
  app:
    <<: *common
    image: nginx:alpine
`
	onLan, err := InjectNetworks(anchored, []Attachment{{Network: "lan", IP: "10.0.1.88"}})
	if err != nil {
		t.Fatalf("InjectNetworks on anchored service: %v", err)
	}
	// Inherited ports from *common should be stashed into x-fjord-published on app.
	if len(PublishedPorts(onLan, nil)) != 0 {
		t.Errorf("expected live published ports to be stashed when IP is pinned:\n%s", onLan)
	}
	detached, err := DetachNetworks(onLan)
	if err != nil {
		t.Fatalf("DetachNetworks on anchored service: %v", err)
	}
	ports := PublishedPorts(detached, nil)
	if len(ports) != 1 || ports[0].Host != 8080 {
		t.Errorf("PublishedPorts after DetachNetworks = %+v, want [8080]:\n%s", ports, detached)
	}
}

func TestBridgeAndPastaNetworkModesReplacedAndCleared(t *testing.T) {
	for _, startMode := range []string{"bridge", "pasta", "slirp4netns"} {
		in := "services:\n  app:\n    image: nginx\n    network_mode: " + startMode + "\n    ports:\n      - \"8080:80\"\n"

		hosted, err := HostNetwork(in)
		if err != nil {
			t.Fatalf("HostNetwork(%s): %v", startMode, err)
		}
		if got := NetworkMode(hosted); got != Host {
			t.Errorf("HostNetwork(%s): got mode %q, want %q:\n%s", startMode, got, Host, hosted)
		}

		disabled, err := DisableNetwork(in)
		if err != nil {
			t.Fatalf("DisableNetwork(%s): %v", startMode, err)
		}
		if got := NetworkMode(disabled); got != None {
			t.Errorf("DisableNetwork(%s): got mode %q, want %q:\n%s", startMode, got, None, disabled)
		}

		bridged, err := SetServiceModes(in, map[string]string{"app": "bridge"})
		if err != nil {
			t.Fatalf("SetServiceModes(%s -> bridge): %v", startMode, err)
		}
		if strings.Contains(bridged, "network_mode") {
			t.Errorf("SetServiceModes(%s -> bridge) left network_mode:\n%s", startMode, bridged)
		}

		detached, err := DetachNetworks(in)
		if err != nil {
			t.Fatalf("DetachNetworks(%s): %v", startMode, err)
		}
		if strings.Contains(detached, "network_mode") {
			t.Errorf("DetachNetworks(%s) left network_mode:\n%s", startMode, detached)
		}

		attached, err := InjectNetworks(in, []Attachment{{Network: "lan", IP: "10.0.1.55"}})
		if err != nil {
			t.Fatalf("InjectNetworks(%s): %v", startMode, err)
		}
		if strings.Contains(attached, "network_mode") {
			t.Errorf("InjectNetworks(%s) left network_mode:\n%s", startMode, attached)
		}
		if got := AttachedNetworks(attached); len(got) != 1 || got[0].Network != "lan" || got[0].IP != "10.0.1.55" {
			t.Errorf("InjectNetworks(%s) attachments = %+v:\n%s", startMode, got, attached)
		}
	}
}

func TestPerServiceOwnNetworkKeepAndRemove(t *testing.T) {
	const in = `services:
  app:
    image: mealie
    ports:
      - "9000:9000"
    networks:
      - mealie_internal
  db:
    image: postgres
    networks:
      - mealie_internal
networks:
  mealie_internal:
    driver: bridge
`
	// (a) Per-service InjectNetworks keeps an own network when included in attachments
	// without turning its top-level declaration into external: true.
	kept, err := InjectNetworks(in, []Attachment{
		{Service: "app", Network: "lan", IP: "10.0.1.40"},
		{Service: "app", Network: "mealie_internal"},
	})
	if err != nil {
		t.Fatalf("InjectNetworks(keep own): %v", err)
	}
	mKept := mustParse(t, kept)
	internalDef, ok := mKept["networks"].(map[string]any)["mealie_internal"].(map[string]any)
	if !ok || internalDef["driver"] != "bridge" || internalDef["external"] != nil {
		t.Fatalf("top-level mealie_internal was altered: %#v\n%s", internalDef, kept)
	}
	appAtts := ServiceAttachments(kept)["app"]
	if len(appAtts) != 2 || appAtts[0].Network != "lan" || appAtts[1].Network != "mealie_internal" || !appAtts[1].Own {
		t.Fatalf("app attachments = %+v, want [lan mealie_internal(own)]:\n%s", appAtts, kept)
	}

	// (b) Per-service InjectNetworks removes an own network from a service when omitted
	// from that service's attachments.
	removed, err := InjectNetworks(kept, []Attachment{
		{Service: "app", Network: "lan", IP: "10.0.1.40"},
	})
	if err != nil {
		t.Fatalf("InjectNetworks(remove own): %v", err)
	}
	appAttsAfter := ServiceAttachments(removed)["app"]
	if len(appAttsAfter) != 1 || appAttsAfter[0].Network != "lan" {
		t.Fatalf("expected mealie_internal removed from app, got %+v:\n%s", appAttsAfter, removed)
	}
	// db was not targeted and still has mealie_internal.
	dbAttsAfter := ServiceAttachments(removed)["db"]
	if len(dbAttsAfter) != 1 || dbAttsAfter[0].Network != "mealie_internal" || !dbAttsAfter[0].Own {
		t.Fatalf("expected db to keep mealie_internal, got %+v:\n%s", dbAttsAfter, removed)
	}
}

func TestDetachNetworksNeverLeavesUndeclaredDefault(t *testing.T) {
	// Service with default + lan (no aliases): DetachNetworks must remove default from
	// both the service and top-level networks.
	withBridgeAndLan, err := InjectNetworks(caddyCompose, []Attachment{
		{Service: "caddy", Network: Bridge},
		{Service: "caddy", Network: "lan", IP: "10.0.1.70"},
	})
	if err != nil {
		t.Fatalf("InjectNetworks: %v", err)
	}
	detached, err := DetachNetworks(withBridgeAndLan)
	if err != nil {
		t.Fatalf("DetachNetworks: %v", err)
	}
	m := mustParse(t, detached)
	svc := m["services"].(map[string]any)["caddy"].(map[string]any)
	if svc["networks"] != nil {
		t.Errorf("expected caddy.networks to be removed after DetachNetworks, got %#v:\n%s", svc["networks"], detached)
	}
	if m["networks"] != nil {
		t.Errorf("expected top-level networks to be removed after DetachNetworks, got %#v:\n%s", m["networks"], detached)
	}

	// Service with undeclared default in input (no aliases): DetachNetworks cleans it up.
	const undeclaredDefault = `services:
  app:
    image: nginx
    networks:
      - default
`
	detached2, err := DetachNetworks(undeclaredDefault)
	if err != nil {
		t.Fatalf("DetachNetworks(undeclaredDefault): %v", err)
	}
	m2 := mustParse(t, detached2)
	if m2["services"].(map[string]any)["app"].(map[string]any)["networks"] != nil {
		t.Errorf("expected undeclared default to be removed from app.networks:\n%s", detached2)
	}
}

func TestAnchorOwnerMutationDoesNotCorruptAliasingService(t *testing.T) {
	const ownerAnchored = `services:
  svc1: &base
    image: nginx:alpine
    ports:
      - "8080:80"
  svc2:
    <<: *base
    image: caddy:alpine
`
	// Mutating svc1 (which owns &base) via SetServiceModes must not strip ports from svc2
	// or leak network_mode: host onto svc2.
	moded, err := SetServiceModes(ownerAnchored, map[string]string{"svc1": "host"})
	if err != nil {
		t.Fatalf("SetServiceModes(svc1=host): %v", err)
	}
	svcs := ParseServices(moded, nil)
	if len(svcs) != 2 {
		t.Fatalf("ParseServices: got %d services, want 2", len(svcs))
	}
	byName := map[string]Service{}
	for _, s := range svcs {
		byName[s.Name] = s
	}
	if !byName["svc1"].NetworkHost || len(byName["svc1"].Ports) != 0 {
		t.Errorf("svc1 = %+v, want NetworkHost=true and no live ports:\n%s", byName["svc1"], moded)
	}
	if byName["svc2"].NetworkHost {
		t.Errorf("svc2 inherited network_mode: host from mutated anchor owner:\n%s", moded)
	}
	if len(byName["svc2"].Ports) != 1 || byName["svc2"].Ports[0].Host != 8080 || byName["svc2"].Ports[0].Container != 80 {
		t.Errorf("svc2 lost its inherited ports 8080:80: %+v\n%s", byName["svc2"].Ports, moded)
	}

	// Same check when mutating svc1 via per-service InjectNetworks.
	injected, err := InjectNetworks(ownerAnchored, []Attachment{{Service: "svc1", Network: "lan", IP: "10.0.1.90"}})
	if err != nil {
		t.Fatalf("InjectNetworks(svc1): %v", err)
	}
	svcs2 := ParseServices(injected, nil)
	byName2 := map[string]Service{}
	for _, s := range svcs2 {
		byName2[s.Name] = s
	}
	if len(byName2["svc1"].Ports) != 0 {
		t.Errorf("svc1 ports should be stashed after InjectNetworks:\n%s", injected)
	}
	if len(byName2["svc2"].Ports) != 1 || byName2["svc2"].Ports[0].Host != 8080 {
		t.Errorf("svc2 lost its inherited ports 8080:80 after InjectNetworks on svc1: %+v\n%s", byName2["svc2"].Ports, injected)
	}
}

func TestDetachNetworksPreservesCustomDefaultNetwork(t *testing.T) {
	const customDefault = `services:
  app:
    image: nginx
    networks:
      - lan
  db:
    image: postgres:16
networks:
  lan:
    external: true
  default:
    ipam:
      config:
        - subnet: 10.89.7.0/24
`
	detached, err := DetachNetworks(customDefault)
	if err != nil {
		t.Fatalf("DetachNetworks: %v", err)
	}
	m := mustParse(t, detached)
	nets, ok := m["networks"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level networks to be kept for custom default:\n%s", detached)
	}
	if _, hasLan := nets["lan"]; hasLan {
		t.Errorf("expected external lan network to be removed on detach:\n%s", detached)
	}
	def, ok := nets["default"].(map[string]any)
	if !ok || def["ipam"] == nil {
		t.Errorf("expected custom default network ipam config to be preserved, got %#v:\n%s", nets["default"], detached)
	}
}

func TestDefaultAliasesNotCopiedToExternalLan(t *testing.T) {
	const withDefaultAliases = `services:
  app:
    image: gitea
    ports:
      - "3000:3000"
  db:
    image: postgres:16
    networks:
      default:
        aliases:
          - db
          - postgres
`
	onLan, err := InjectNetworks(withDefaultAliases, []Attachment{{Network: "lan", IP: "10.0.1.50"}})
	if err != nil {
		t.Fatalf("InjectNetworks: %v", err)
	}
	m := mustParse(t, onLan)
	dbNets := m["services"].(map[string]any)["db"].(map[string]any)["networks"]
	// db should join lan as a plain list [- lan], never with aliases: [db, postgres] on shared lan.
	list, ok := dbNets.([]any)
	if !ok || len(list) != 1 || list[0] != "lan" {
		t.Errorf("db.networks on lan = %#v, want plain [lan] without default aliases:\n%s", dbNets, onLan)
	}
}

func TestSubtreeAnchorReferencedOutsideServicesAndUntouchedSiblings(t *testing.T) {
	const withTopLevelAlias = `x-common: &common
  restart: unless-stopped

services:
  app:
    <<: *common
    image: nginx
    ports:
      - "8080:80"
    environment: &env
      TZ: UTC
  worker:
    <<: *common
    image: alpine

x-copy:
  environment: *env
`
	// Moving only app to host must produce valid YAML even when x-copy references *env
	// inside services.app, and must leave untouched worker's <<: *common merge intact.
	out, err := SetServiceModes(withTopLevelAlias, map[string]string{"app": "host"})
	if err != nil {
		t.Fatalf("SetServiceModes: %v", err)
	}
	m := mustParse(t, out)
	xCopy := m["x-copy"].(map[string]any)["environment"].(map[string]any)
	if xCopy["TZ"] != "UTC" {
		t.Errorf("x-copy.environment.TZ = %#v, want UTC:\n%s", xCopy["TZ"], out)
	}
	if !strings.Contains(out, "<<: *common") {
		t.Errorf("untouched worker should keep <<: *common merge:\n%s", out)
	}

	// SetRepoTag on a matching repo must not copy inherited keys into an untouched service.
	const repoAnchored = `x-common: &common
  image: example/other:1
  restart: unless-stopped

services:
  app:
    image: ghcr.io/daemonless/radarr:latest
  side:
    <<: *common
`
	retagged, err := SetRepoTag(repoAnchored, "ghcr.io/daemonless/radarr", "pkg")
	if err != nil {
		t.Fatalf("SetRepoTag: %v", err)
	}
	if strings.Contains(retagged, "side:\n    <<: *common\n    image:") {
		t.Errorf("SetRepoTag copied inherited image onto untouched side service:\n%s", retagged)
	}
}
