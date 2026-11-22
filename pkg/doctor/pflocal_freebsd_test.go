package doctor

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPfLocalStatus(t *testing.T) {
	for _, c := range []struct {
		v             string
		hooked, skips bool
		want          Status
	}{
		{"1", true, false, OK},
		{"1", false, false, Warn},
		{"0", false, false, Warn},
		{"1", true, true, Warn},
		{"", false, false, Unknown},
		{"", false, true, Unknown},
	} {
		if st, _ := pfLocalStatus(c.v, c.hooked, c.skips); st != c.want {
			t.Errorf("%q hooked=%v skips=%v: got %v, want %v", c.v, c.hooked, c.skips, st, c.want)
		}
	}
}

func TestPfSkips(t *testing.T) {
	if !pfSkips("No ALTQ support in kernel\nlo0 (skip)\n\tCleared:     Sun Nov 22\n", "lo0") {
		t.Error("a skipped lo0 was not seen")
	}
	if pfSkips("lo0\n\tCleared:     Sun Nov 22\n", "lo0") {
		t.Error("lo0 read as skipped")
	}
	if pfSkips("lo1 (skip)\n", "lo0") {
		t.Error("another interface's skip taken for lo0's")
	}
}

func TestPfLocalHooked(t *testing.T) {
	const head = " Intercept point     Type Dir             Hook\n"
	on := head +
		"     inet6-local     IPv6\n                          Out               pf:default-out6\n" +
		"      inet-local     IPv4\n                          Out               pf:default-out\n" +
		"            inet     IPv4\n                           In               pf:default-in\n"
	off := head +
		"     inet6-local     IPv6\n" +
		"      inet-local     IPv4\n" +
		"            inet     IPv4\n                           In               pf:default-in\n                          Out               pf:default-out\n" +
		"        ethernet Ethernet\n"
	if !pfLocalHooked(on) {
		t.Error("a pf hook under inet-local was not seen")
	}
	if pfLocalHooked(off) {
		t.Error("pf's hooks under inet were taken for inet-local's")
	}
	if pfLocalHooked("") {
		t.Error("no output read as hooked")
	}
}

func TestKeepSysctl(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sysctl.conf")
	for _, c := range []struct{ before, want string }{
		{"", "net.pf.filter_local=1\n"},
		{"# mine\nkern.x=2\n", "# mine\nkern.x=2\nnet.pf.filter_local=1\n"},
		{"kern.x=2\nnet.pf.filter_local=0\n", "kern.x=2\nnet.pf.filter_local=1\n"},
		{"net.pf.filter_local=1\n", "net.pf.filter_local=1\n"},
	} {
		os.Remove(path)
		if c.before != "" {
			if err := os.WriteFile(path, []byte(c.before), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := keepSysctl(io.Discard, path, "net.pf.filter_local", "1"); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != c.want {
			t.Errorf("before %q: got %q, want %q", c.before, got, c.want)
		}
	}
}
