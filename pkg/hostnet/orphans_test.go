package hostnet

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// saturn's lan after tautulli's update under the old cni-epair: .200 held by
// the container the update replaced, .201 by the new one.
func TestReleaseOrphans(t *testing.T) {
	dir := t.TempDir()
	old := ipamStateDir
	ipamStateDir = dir
	t.Cleanup(func() { ipamStateDir = old })

	lan := filepath.Join(dir, "lan")
	os.MkdirAll(lan, 0o755)
	write := func(name, body string, age time.Duration) {
		p := filepath.Join(lan, name)
		os.WriteFile(p, []byte(body), 0o644)
		when := time.Now().Add(-age)
		os.Chtimes(p, when, when)
	}
	write("192.168.86.200", "ef883aeb\r\neth0", time.Hour)      // gone container: orphan
	write("192.168.86.201", "a9aeffba\r\neth0", time.Hour)      // tautulli, running
	write("192.168.86.202", "c0ffee00\r\neth0", 10*time.Second) // being created elsewhere right now
	write("last_reserved_ip.0", "192.168.86.202", time.Hour)    // host-local's own state
	write("lock", "", time.Hour)

	live := func(id string) bool { return id == "a9aeffba" }
	if got := ReleaseOrphans("lan", live, 2*time.Minute); !reflect.DeepEqual(got, []string{"192.168.86.200"}) {
		t.Fatalf("freed %v, want only 192.168.86.200", got)
	}
	for _, keep := range []string{"192.168.86.201", "192.168.86.202", "last_reserved_ip.0", "lock"} {
		if _, err := os.Stat(filepath.Join(lan, keep)); err != nil {
			t.Errorf("%s was removed", keep)
		}
	}
	if got := ReleaseOrphans("../etc", live, 0); got != nil {
		t.Errorf("a network name that is a path was accepted: %v", got)
	}
}

// A start whose bridge add failed left 10.100.0.4 reserved by the container
// itself, with no cached result: every retry was refused with "duplicate
// allocation" (immich_database_1 on netlab, 2026-09-21).
func TestReleaseFailedAdds(t *testing.T) {
	dir, results := t.TempDir(), t.TempDir()
	oldState, oldResults := ipamStateDir, cniResultsDir
	ipamStateDir, cniResultsDir = dir, results
	t.Cleanup(func() { ipamStateDir, cniResultsDir = oldState, oldResults })

	priv := filepath.Join(dir, "immich_priv")
	os.MkdirAll(priv, 0o755)
	os.WriteFile(filepath.Join(priv, "10.100.0.4"), []byte("c51dd19e\r\neth1"), 0o644) // failed add, stopped
	os.WriteFile(filepath.Join(priv, "10.100.0.5"), []byte("aa11bb22\r\neth1"), 0o644) // stopped, but its add completed
	os.WriteFile(filepath.Join(priv, "10.100.0.6"), []byte("deadbeef\r\neth1"), 0o644) // another stack's
	os.WriteFile(filepath.Join(results, "immich_priv-aa11bb22-eth1"), []byte("{}"), 0o644)

	stopped := func(id string) bool { return id == "c51dd19e" || id == "aa11bb22" }
	if got := ReleaseFailedAdds("immich_priv", stopped); !reflect.DeepEqual(got, []string{"10.100.0.4"}) {
		t.Fatalf("freed %v, want only 10.100.0.4", got)
	}
	for _, keep := range []string{"10.100.0.5", "10.100.0.6"} {
		if _, err := os.Stat(filepath.Join(priv, keep)); err != nil {
			t.Errorf("%s removed", keep)
		}
	}
}
