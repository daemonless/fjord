package doctor

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The loaded ruleset of a FreeBSD host on netavark, as pfctl prints it.
const (
	netavarkNat    = "nat-anchor \"netavark/*\" all\nrdr-anchor \"netavark/*\" all\n"
	netavarkRules  = "anchor \"netavark/*\" all\npass all flags S/SA keep state\n"
	netavarkTables = "netavark-isolated\nnetavark-subnets\n"
)

func TestNetavarkPfMissing(t *testing.T) {
	if miss := netavarkPfMissing(netavarkNat, netavarkRules, netavarkTables); len(miss) != 0 {
		t.Errorf("a complete ruleset reported missing %v", miss)
	}
	// CNI's lines are not netavark's.
	cniNat := "nat on vtnet0 inet from <cni-nat> to any -> (vtnet0)\nrdr-anchor \"cni-rdr/*\" all\nnat-anchor \"cni-rdr/*\" all\n"
	miss := netavarkPfMissing(cniNat, "pass all\n", "cni-nat\n")
	for _, want := range []string{"table <netavark-subnets>", "table <netavark-isolated>", `nat-anchor "netavark/*"`, `rdr-anchor "netavark/*"`, `anchor "netavark/*"`} {
		if !slices.Contains(miss, want) {
			t.Errorf("a CNI ruleset: %q not reported, got %v", want, miss)
		}
	}
	// The translation anchors alone do not filter: nat-anchor is not anchor.
	if miss := netavarkPfMissing(netavarkNat, "pass all\n", netavarkTables); !slices.Equal(miss, []string{`anchor "netavark/*"`}) {
		t.Errorf("filter anchor missing: got %v", miss)
	}
}

// The lines go in as one block before the first filter rule, which keeps
// pf's order (tables, translation, filter) and puts netavark's filter anchor
// ahead of the host's own rules. A second run adds nothing.
func TestNetavarkPfInsert(t *testing.T) {
	conf := "ext_if = \"vtnet0\"\nnat on $ext_if from 10.0.0.0/8 to any -> ($ext_if)\npass all\n"
	got, added := pfInsert(conf, netavarkPfRules())
	if len(added) != 5 {
		t.Fatalf("added %v", added)
	}
	lines := strings.Split(strings.TrimSpace(got), "\n")
	idx := func(l string) int { return slices.Index(lines, l) }
	if !(idx(`nat on $ext_if from 10.0.0.0/8 to any -> ($ext_if)`) < idx(`nat-anchor "netavark/*"`) &&
		idx(`table <netavark-subnets> persist`) < idx(`nat-anchor "netavark/*"`) &&
		idx(`rdr-anchor "netavark/*"`) < idx(`anchor "netavark/*"`) &&
		idx(`anchor "netavark/*"`) < idx(`pass all`)) {
		t.Errorf("order:\n%s", got)
	}
	if _, again := pfInsert(got, netavarkPfRules()); len(again) != 0 {
		t.Errorf("second run added %v", again)
	}
}

func TestForNetavark(t *testing.T) {
	in := []Check{{ID: "podman"}, {ID: "epair"}, {ID: "dnsname", Name: "container name resolution (dnsname plugin)"}, {ID: "pf"}, {ID: "appjail-pf"}}
	out := forNetavark(in, "")
	var ids []string
	for _, c := range out {
		ids = append(ids, c.ID)
	}
	if !slices.Equal(ids, []string{"podman", "dnsname", "dns-port", "pf", "appjail-pf"}) {
		t.Fatalf("ids: %v", ids)
	}
	if in[2].Name != "container name resolution (dnsname plugin)" {
		t.Error("the caller's list was changed")
	}
	dns, pf := out[1], out[3]
	if !strings.Contains(dns.Name, "aardvark-dns") || dns.Pkg["freebsd"] != "aardvark-dns" || strings.Contains(dns.Fix, "cni") {
		t.Errorf("dns check: %+v", dns)
	}
	cmds := strings.Join(pf.Commands, "\n")
	if !strings.Contains(cmds, `anchor "netavark/*"`) || strings.Contains(cmds, "cni") || strings.Contains(pf.Fix, "cni") {
		t.Errorf("pf check still talks about CNI:\n%s\n%s", cmds, pf.Fix)
	}
	if pf.Install == nil || pf.Probe == nil {
		t.Error("pf check lost its probe or install")
	}
}

func TestAardvarkProbe(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "aardvark-dns")
	if st, _ := aardvarkProbe(bin)(context.Background()); st == OK {
		if _, err := os.Stat("/usr/local/libexec/podman/aardvark-dns"); err != nil {
			if _, err := os.Stat("/usr/local/bin/aardvark-dns"); err != nil {
				t.Error("OK with no aardvark-dns anywhere")
			}
		}
	}
	if err := os.WriteFile(bin, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if st, msg := aardvarkProbe(bin)(context.Background()); st != OK {
		t.Errorf("podman's own path exists: %v %s", st, msg)
	}
}
