package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// runSnippet pastes a pf.conf edit into sh, as the operator would.
func runSnippet(t *testing.T, snippet string) {
	t.Helper()
	if out, err := exec.Command("sh", "-c", snippet).CombinedOutput(); err != nil {
		t.Fatalf("snippet failed: %v\n%s\n--- snippet:\n%s", err, out, snippet)
	}
}

var (
	podmanRules  = []string{`rdr-anchor "cni-rdr/*"`, `nat-anchor "cni-rdr/*"`}
	appjailRules = []string{`nat-anchor "appjail-nat/jail/*"`, `rdr-anchor "appjail-rdr/*"`}
)

// A fresh host has no /etc/pf.conf: the snippet has to create it (it used to
// fail on the missing file and leave nothing behind), and the second engine's
// snippet has to add to it, not replace it.
func TestPfSnippetOnAHostWithoutPfConf(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "pf.conf")
	runSnippet(t, pfInsertSnippet(podmanRules, conf))
	runSnippet(t, pfInsertSnippet(appjailRules, conf))
	got, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range append(podmanRules, appjailRules...) {
		if !strings.Contains(string(got), r) {
			t.Errorf("pf.conf lacks %q:\n%s", r, got)
		}
	}
}

// With filter rules present, translation rules go before the first one, or pf
// rejects the file ("Rules must be in order").
func TestPfSnippetInsertsBeforeFilterRules(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "pf.conf")
	os.WriteFile(conf, []byte("ext_if=\"vtnet0\"\npass all\n"), 0o644)
	runSnippet(t, pfInsertSnippet(podmanRules, conf))
	got, _ := os.ReadFile(conf)
	if a, p := strings.Index(string(got), "rdr-anchor"), strings.Index(string(got), "pass all"); a < 0 || a > p {
		t.Fatalf("anchors must come before the first filter rule:\n%s", got)
	}
	if !strings.HasPrefix(string(got), "ext_if=") {
		t.Fatalf("existing lines must be kept in place:\n%s", got)
	}
}

// fjordfresh: vtnet0 on VLAN 4 (default route), vtnet1 on the main LAN where
// the DNS server is. NAT on vtnet0 alone broke every bridge container there.
func TestNatCandidatesEveryHostNetwork(t *testing.T) {
	got := natCandidates([]string{"lo0", "vtnet1", "cni-podman0", "vtnet0", "epair0a", "ajnet", "tailscale0"}, "vtnet0", []string{"ajnet"})
	if want := []string{"vtnet0", "vtnet1", "tailscale0"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMissingNat(t *testing.T) {
	active := "nat-anchor \"cni-rdr/*\" all\nnat on vtnet0 inet from <cni-nat> to any -> (vtnet0) round-robin\nrdr-anchor \"cni-rdr/*\" all\n"
	if got := missingNat(active, []string{"vtnet0", "vtnet1"}); !slices.Equal(got, []string{"vtnet1"}) {
		t.Fatalf("got %v, want [vtnet1]", got)
	}
	// vtnet1 must not count as covered by a rule for vtnet10.
	if got := missingNat("nat on vtnet10 inet from <cni-nat> to any -> (vtnet10)\n", []string{"vtnet1"}); !slices.Equal(got, []string{"vtnet1"}) {
		t.Fatalf("vtnet10 rule counted for vtnet1: %v", got)
	}
	if got := missingNat(active+"nat on vtnet1 inet from <cni-nat> to any -> (vtnet1) round-robin\n", []string{"vtnet0", "vtnet1"}); len(got) != 0 {
		t.Fatalf("all covered, got %v", got)
	}
}

// The commands are pasted more than once (a check that is still red invites
// it); the first version appended every line again each time, six copies.
// Running them again must change nothing, and a file that already has some
// of the lines only gets the rest.
func TestPfSnippetIsSafeToRunAgain(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "pf.conf")
	rules := append(podmanRules, "nat on vtnet0 inet from <cni-nat> to any -> (vtnet0)", "nat on vtnet1 inet from <cni-nat> to any -> (vtnet1)")
	runSnippet(t, pfInsertSnippet(rules, conf))
	once, _ := os.ReadFile(conf)
	runSnippet(t, pfInsertSnippet(rules, conf))
	runSnippet(t, pfInsertSnippet(rules, conf))
	if again, _ := os.ReadFile(conf); string(again) != string(once) {
		t.Fatalf("second and third runs changed the file:\n--- once:\n%s--- after three:\n%s", once, again)
	}

	// Only the missing line is added, before the filter rules.
	os.WriteFile(conf, []byte(`rdr-anchor "cni-rdr/*"`+"\nnat on vtnet0 inet from <cni-nat> to any -> (vtnet0)\npass all\n"), 0o644)
	runSnippet(t, pfInsertSnippet([]string{"nat on vtnet0 inet from <cni-nat> to any -> (vtnet0)", "nat on vtnet1 inet from <cni-nat> to any -> (vtnet1)"}, conf))
	got, _ := os.ReadFile(conf)
	want := `rdr-anchor "cni-rdr/*"` + "\nnat on vtnet0 inet from <cni-nat> to any -> (vtnet0)\nnat on vtnet1 inet from <cni-nat> to any -> (vtnet1)\npass all\n"
	if string(got) != want {
		t.Fatalf("got:\n%swant:\n%s", got, want)
	}
}
