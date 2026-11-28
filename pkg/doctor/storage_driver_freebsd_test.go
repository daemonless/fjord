//go:build freebsd

package doctor

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pkgStorageConf = `[storage]
# Default Storage Driver
driver = "zfs"

runroot = "/var/run/containers/storage"
graphroot = "%s"
`

func writeConf(t *testing.T, driver, root string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "storage.conf")
	body := strings.Replace(pkgStorageConf, `driver = "zfs"`, `driver = "`+driver+`"`, 1)
	body = strings.Replace(body, "%s", root, 1)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStorageDriverStatus(t *testing.T) {
	root := t.TempDir()
	on := func(fs string) func(string) (string, error) { return func(string) (string, error) { return fs, nil } }
	cases := []struct {
		name   string
		driver string
		fs     func(string) (string, error)
		want   Status
	}{
		{"zfs driver on ZFS", "zfs", on("zfs"), OK},
		{"zfs driver on UFS", "zfs", on("ufs"), Fail},
		{"vfs driver on UFS", "vfs", on("ufs"), OK},
		{"filesystem unknown", "zfs", func(string) (string, error) { return "", errors.New("no") }, Warn},
	}
	for _, c := range cases {
		got, msg := storageDriverStatus(writeConf(t, c.driver, root), c.fs)
		if got != c.want {
			t.Errorf("%s: %v (%s), want %v", c.name, got, msg, c.want)
		}
	}
	if got, _ := storageDriverStatus(filepath.Join(t.TempDir(), "none.conf"), on("ufs")); got != OK {
		t.Errorf("no storage.conf: %v, want OK", got)
	}
}

// Before first use the storage folder is not there: the nearest folder
// that is decides.
func TestStorageDriverStatusRootNotMadeYet(t *testing.T) {
	parent := t.TempDir()
	var asked string
	_, _ = storageDriverStatus(writeConf(t, "zfs", filepath.Join(parent, "a", "b")), func(p string) (string, error) { asked = p; return "ufs", nil })
	if asked != parent {
		t.Errorf("asked about %q, want %q", asked, parent)
	}
}

func TestUseVFS(t *testing.T) {
	p := writeConf(t, "zfs", "/var/db/containers/storage")
	before, _ := os.ReadFile(p)
	if err := useVFS(p, io.Discard); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p)
	if storageDriver(string(after)) != "vfs" {
		t.Errorf("driver now %q, want vfs", storageDriver(string(after)))
	}
	if !strings.Contains(string(after), `graphroot = "/var/db/containers/storage"`) || !strings.Contains(string(after), "# Default Storage Driver") {
		t.Errorf("the rest of the file changed:\n%s", after)
	}
	bak, _ := os.ReadFile(p + ".fjord-bak")
	if string(bak) != string(before) {
		t.Errorf("backup is not the old file")
	}
	// A second run leaves a non-zfs driver alone.
	if err := useVFS(p, io.Discard); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(p)
	if string(again) != string(after) {
		t.Errorf("second run changed the file")
	}
}

// The folder a failed zfs start left is moved aside on UFS, kept on ZFS,
// and a second move does not overwrite the first.
func TestMoveFailedStore(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "storage")
	mk := func() {
		if err := os.MkdirAll(filepath.Join(root, "libpod"), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(root, "db.sql"), []byte("zfs"), 0o644)
	}
	on := func(fs string) func(string) (string, error) { return func(string) (string, error) { return fs, nil } }
	mk()
	if err := moveFailedStore(root, on("zfs"), io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("on ZFS the folder was moved")
	}
	if err := moveFailedStore(root, on("ufs"), io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err == nil {
		t.Errorf("on UFS the folder is still there")
	}
	if b, _ := os.ReadFile(filepath.Join(root+".fjord-zfs", "db.sql")); string(b) != "zfs" {
		t.Errorf("moved folder lost its contents")
	}
	mk()
	if err := moveFailedStore(root, on("ufs"), io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root+".fjord-zfs", "db.sql")); string(b) != "zfs" {
		t.Errorf("second move overwrote the first")
	}
	if err := moveFailedStore(filepath.Join(dir, "none"), on("ufs"), io.Discard); err != nil {
		t.Errorf("a folder not made yet: %v", err)
	}
}

// The driver already says vfs, but the folder was made for zfs on UFS:
// podman's database puts zfs back, so the check still fails.
func TestStorageDriverStatusLeftoverZFSStore(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "zfs"), 0o755); err != nil {
		t.Fatal(err)
	}
	ufs := func(string) (string, error) { return "ufs", nil }
	if got, msg := storageDriverStatus(writeConf(t, "vfs", root), ufs); got != Fail {
		t.Errorf("vfs over a zfs-made folder on UFS: %v (%s), want Fail", got, msg)
	}
	zfs := func(string) (string, error) { return "zfs", nil }
	if got, msg := storageDriverStatus(writeConf(t, "vfs", root), zfs); got != OK {
		t.Errorf("the same on ZFS: %v (%s), want OK", got, msg)
	}
}
