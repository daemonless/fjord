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
