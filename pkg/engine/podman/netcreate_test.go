package podman

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
)

func goodSpec() engine.NetworkSpec {
	return engine.NetworkSpec{
		Name: "vlan4", Kind: "lan", Parent: "vlan4bridge",
		Subnet: "192.168.4.0/24", Gateway: "192.168.4.1",
	}
}

// The generated conflist must name the epair plugin and the parent bridge:
// CNI resolves "type" to a binary of that name, so a wrong type silently
// routes the network at whatever else is installed under it.
func TestConflistShape(t *testing.T) {
	data, err := conflist(goodSpec())
	if err != nil {
		t.Fatalf("conflist: %v", err)
	}
	var doc struct {
		CNIVersion string `json:"cniVersion"`
		Name       string `json:"name"`
		Plugins    []struct {
			Type   string `json:"type"`
			Master string `json:"master"`
			MTU    int    `json:"mtu"`
			IPAM   struct {
				Type   string              `json:"type"`
				Ranges [][]map[string]any  `json:"ranges"`
				Routes []map[string]string `json:"routes"`
			} `json:"ipam"`
			Capabilities map[string]bool `json:"capabilities"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, data)
	}
	if doc.Name != "vlan4" || len(doc.Plugins) != 1 {
		t.Fatalf("unexpected doc: %s", data)
	}
	p := doc.Plugins[0]
	if p.Type != "epair" {
		t.Errorf("type = %q, want epair", p.Type)
	}
	if p.Master != "vlan4bridge" {
		t.Errorf("master = %q, want vlan4bridge", p.Master)
	}
	if p.MTU != 0 {
		t.Errorf("mtu emitted when unset: %d", p.MTU)
	}
	if p.IPAM.Type != "host-local" || len(p.IPAM.Ranges) != 1 || len(p.IPAM.Ranges[0]) != 1 {
		t.Fatalf("ipam wrong: %s", data)
	}
	r := p.IPAM.Ranges[0][0]
	if r["subnet"] != "192.168.4.0/24" || r["gateway"] != "192.168.4.1" {
		t.Errorf("range = %v", r)
	}
	if _, ok := r["rangeStart"]; ok {
		t.Errorf("rangeStart emitted when unset: %v", r)
	}
	if !p.Capabilities["ips"] {
		t.Errorf("ips capability missing; compose ipv4_address would not work")
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("conflist should end with a newline")
	}
}

func TestConflistOptionalFields(t *testing.T) {
	s := goodSpec()
	s.MTU, s.RangeStart, s.RangeEnd = 9000, "192.168.4.200", "192.168.4.250"
	data, err := conflist(s)
	if err != nil {
		t.Fatalf("conflist: %v", err)
	}
	for _, want := range []string{`"mtu": 9000`, `"rangeStart": "192.168.4.200"`, `"rangeEnd": "192.168.4.250"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s in:\n%s", want, data)
		}
	}
}

// Bad input must be refused before anything reaches the conf directory: every
// file there is a live network podman reads immediately.
func TestConflistRejects(t *testing.T) {
	cases := map[string]func(*engine.NetworkSpec){
		"empty name":         func(s *engine.NetworkSpec) { s.Name = "" },
		"path traversal":     func(s *engine.NetworkSpec) { s.Name = "../evil" },
		"slash in name":      func(s *engine.NetworkSpec) { s.Name = "a/b" },
		"no parent":          func(s *engine.NetworkSpec) { s.Parent = "" },
		"subnet not a cidr":  func(s *engine.NetworkSpec) { s.Subnet = "192.168.4.0" },
		"gateway not an ip":  func(s *engine.NetworkSpec) { s.Gateway = "nope" },
		"gateway off subnet": func(s *engine.NetworkSpec) { s.Gateway = "10.0.0.1" },
		"range off subnet":   func(s *engine.NetworkSpec) { s.RangeStart = "10.0.0.5" },
	}
	for name, mutate := range cases {
		s := goodSpec()
		mutate(&s)
		if _, err := conflist(s); err == nil {
			t.Errorf("%s: accepted %+v", name, s)
		}
	}
}

// podman's and appjail's own bridges must never be offered as LAN parents.
func TestIsRuntimeBridge(t *testing.T) {
	for _, n := range []string{"cni-podman0", "podman1", "ajnet"} {
		if !isRuntimeBridge(n) {
			t.Errorf("%q should be excluded", n)
		}
	}
	for _, n := range []string{"bridge86", "vlan4bridge", "vlan5bridge"} {
		if isRuntimeBridge(n) {
			t.Errorf("%q should be offered", n)
		}
	}
}
