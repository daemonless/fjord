package doctor

import (
	"slices"
	"strings"
	"testing"
)

var testRules = []string{`rdr-anchor "cni-rdr/*"`, `nat-anchor "cni-rdr/*"`, `table <cni-nat>`, `nat on vtnet0 inet from <cni-nat> to any -> (vtnet0)`}

// A host with no pf.conf gets every rule; running again adds nothing (the
// shell snippet once appended a copy per run).
func TestPfInsertFreshThenNothing(t *testing.T) {
	got, added := pfInsert("", testRules)
	if len(added) != len(testRules) || got != strings.Join(testRules, "\n")+"\n" {
		t.Fatalf("fresh file: added %d, got:\n%s", len(added), got)
	}
	again, added := pfInsert(got, testRules)
	if added != nil || again != got {
		t.Fatalf("second run changed the file: %v\n%s", added, again)
	}
}

// Translation rules go before the first filter rule; lines already there stay
// where they are, and only the missing ones are added.
func TestPfInsertBeforeFilterRulesOnlyWhatIsMissing(t *testing.T) {
	conf := "ext_if=\"vtnet0\"\ntable <cni-nat>\n# allow all\npass all\nblock in on $ext_if proto tcp to port 22\n"
	got, added := pfInsert(conf, testRules)
	want := []string{`rdr-anchor "cni-rdr/*"`, `nat-anchor "cni-rdr/*"`, `nat on vtnet0 inet from <cni-nat> to any -> (vtnet0)`}
	if !slices.Equal(added, want) {
		t.Fatalf("added %q, want %q", added, want)
	}
	if strings.Count(got, "table <cni-nat>") != 1 {
		t.Fatalf("the table line was added twice:\n%s", got)
	}
	if a, p := strings.Index(got, "rdr-anchor"), strings.Index(got, "pass all"); a < 0 || a > p {
		t.Fatalf("anchors must come before the first filter rule:\n%s", got)
	}
	if !strings.HasPrefix(got, "ext_if=\"vtnet0\"\ntable <cni-nat>\n# allow all\n") || !strings.HasSuffix(got, "pass all\nblock in on $ext_if proto tcp to port 22\n") {
		t.Fatalf("existing lines moved:\n%s", got)
	}
}

// A file without a final newline, and one whose filter rules are indented:
// the awk snippet only matched at column one, and so does this.
func TestPfInsertAppendsAfterUnterminatedLine(t *testing.T) {
	got, _ := pfInsert("set skip on lo0", testRules[:1])
	if got != "set skip on lo0\nrdr-anchor \"cni-rdr/*\"\n" {
		t.Fatalf("got:\n%q", got)
	}
	got, _ = pfInsert("  pass all\n", testRules[:1])
	if !strings.HasSuffix(got, "  pass all\nrdr-anchor \"cni-rdr/*\"\n") {
		t.Fatalf("got:\n%q", got)
	}
}
