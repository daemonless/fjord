package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFstabMounts(t *testing.T) {
	fstab := "# Device\tMountpoint\tFStype\n" +
		"fdescfs  /compat/linux/dev/fd   fdescfs  rw,late,linrdlnk       0 0\n" +
		"#fdesc /dev/fd fdescfs rw 0 0\n"
	if fstabMounts(fstab, "/dev/fd", "fdescfs") {
		t.Fatal("a Linux-compat or commented-out line counted")
	}
	if !fstabMounts(fstab+"fdesc\t/dev/fd\t\tfdescfs\trw\t0\t0\n", "/dev/fd", "fdescfs") {
		t.Fatal("the line was not found")
	}
}

// A directory that lists only 0-2, as /dev/fd does without fdescfs.
func TestFdescProbeWithoutFdescfs(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"0", "1", "2"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o644)
	}
	if st, detail := fdescProbe(dir)(context.Background()); st != Warn || !strings.Contains(detail, "not mounted") {
		t.Fatalf("got %s %q", st, detail)
	}
}

// The fstab half of the fix adds one line, once, after a file with no final
// newline, and leaves the rest alone.
func TestAddFdescFstabOnce(t *testing.T) {
	fstab := filepath.Join(t.TempDir(), "fstab")
	os.WriteFile(fstab, []byte("/dev/ada0p2 / ufs rw 1 1"), 0o644)
	for i := 0; i < 2; i++ {
		var out strings.Builder
		if err := addFdescFstab(&out, "/dev/fd", fstab); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(fstab)
	want := "/dev/ada0p2 / ufs rw 1 1\nfdesc\t/dev/fd\t\tfdescfs\trw\t0\t0\n"
	if string(b) != want {
		t.Fatalf("fstab now %q, want %q", b, want)
	}
}
