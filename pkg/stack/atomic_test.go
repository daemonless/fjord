package stack

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// netlab, 2026-09-22: a version change saved zensical's compose while the
// disk was full, and os.WriteFile left it empty -- the running container kept
// going and fjord knew nothing about it any more. A failed save must be an
// error and leave the old file, not a truncated one.
func TestFailedSaveKeepsOldFiles(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	good := &Stack{Name: "zensical", Compose: "services:\n  zensical:\n    image: z:0.0.64\n", Env: "PUID=1000\n"}
	if err := m.Save(good); err != nil {
		t.Fatal(err)
	}

	old := writeData
	t.Cleanup(func() { writeData = old })
	writeData = func(*os.File, []byte) (int, error) { return 0, syscall.ENOSPC }

	err := m.Save(&Stack{Name: "zensical", Compose: "services:\n  zensical:\n    image: z:0.0.63\n", Env: "PUID=1\n"})
	if err == nil {
		t.Fatal("save on a full disk reported success")
	}
	for file, want := range map[string]string{"compose.yaml": good.Compose, ".env": good.Env} {
		got, _ := os.ReadFile(filepath.Join(dir, "zensical", file))
		if string(got) != want {
			t.Errorf("%s after a failed save = %q, want the old %q", file, got, want)
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "zensical", ".*.tmp"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

// Permissions are the caller's, not CreateTemp's 0600: compose stays readable,
// .env and state.json stay private.
func TestAtomicWritePermissions(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	if err := m.Save(&Stack{Name: "s", Compose: "services: {}\n", Env: "A=1\n"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveState("s", &State{}); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]os.FileMode{"compose.yaml": 0o644, ".env": 0o600, "state.json": 0o600} {
		fi, err := os.Stat(filepath.Join(dir, "s", file))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", file, fi.Mode().Perm(), want)
		}
	}
}
