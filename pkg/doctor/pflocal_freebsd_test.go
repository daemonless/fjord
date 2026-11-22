package doctor

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPfLocalStatus(t *testing.T) {
	for _, c := range []struct {
		v      string
		hooked bool
		want   Status
	}{{"1", true, OK}, {"1", false, Warn}, {"0", false, Warn}, {"", false, Unknown}} {
		if st, _ := pfLocalStatus(c.v, c.hooked); st != c.want {
			t.Errorf("%q hooked=%v: got %v, want %v", c.v, c.hooked, st, c.want)
		}
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
