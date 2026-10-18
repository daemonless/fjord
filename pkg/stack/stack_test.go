package stack

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateEngineRecordsOnlyMissing(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	for _, n := range []string{"old", "new"} {
		os.MkdirAll(filepath.Join(dir, n), 0o755)
		os.WriteFile(filepath.Join(dir, n, "compose.yaml"), []byte("services: {}\n"), 0o644)
	}
	m.SaveState("new", &State{SchemaVersion: stateSchemaVersion, Engine: "appjail"})
	migrated, err := m.MigrateEngine("podman")
	if err != nil {
		t.Fatal(err)
	}
	if len(migrated) != 1 || migrated[0] != "old" {
		t.Fatalf("migrated = %v, want [old]", migrated)
	}
	if st, _ := m.LoadState("old"); st == nil || st.Engine != "podman" {
		t.Fatalf("old: engine not recorded: %+v", st)
	}
	if st, _ := m.LoadState("new"); st.Engine != "appjail" {
		t.Fatalf("new: engine changed to %q", st.Engine)
	}
}

func TestAllocateNameSlugsAndSuffixes(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	if got := m.AllocateName("Tautulli 4K", "tautulli"); got != "tautulli-4k" {
		t.Fatalf("slug = %q", got)
	}
	if got := m.AllocateName("!!!", "radarr"); got != "radarr" {
		t.Fatalf("fallback = %q", got)
	}
	os.MkdirAll(filepath.Join(dir, "radarr"), 0o755)
	if got := m.AllocateName("Radarr", "radarr"); got != "radarr-2" {
		t.Fatalf("second radarr = %q, want radarr-2", got)
	}
	os.RemoveAll(filepath.Join(dir, "radarr"))
	if got := m.AllocateName("Radarr", "radarr"); got != "radarr" {
		t.Fatalf("after delete = %q, want radarr (name is free again)", got)
	}
}

// Saving a director stack again leaves an unchanged Makejail alone: director
// rebuilds every service built from it when the file is newer than its last
// run (a rollback of one sidecar restarted the app, netlab 2026-10-18).
func TestSaveKeepsUnchangedMakejail(t *testing.T) {
	m := NewManager(t.TempDir())
	st := &Stack{Name: "aj", Director: "services:\n  web:\n    name: aj_web\n", Makejail: "OPTION from=ghcr.io/x/web:${tag}\n"}
	if err := m.Save(st); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(st.Dir, "Makejail")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	st.Director = "services:\n  web:\n    name: aj_web\n    arguments:\n      - tag: 'pkg'\n"
	if err := m.Save(st); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || !fi.ModTime().Equal(old) {
		t.Fatalf("the Makejail was rewritten: %v %v", fi.ModTime(), err)
	}
	st.Makejail = "OPTION from=ghcr.io/x/web2:${tag}\n"
	if err := m.Save(st); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != st.Makejail {
		t.Fatalf("a changed Makejail was not written: %q", b)
	}
}
