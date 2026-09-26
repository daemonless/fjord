package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
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
