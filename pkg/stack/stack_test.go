package stack

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
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
	alloc := func(name, fallback string) string {
		id, release := m.AllocateName(name, fallback)
		release()
		return id
	}
	if got := alloc("Tautulli 4K", "tautulli"); got != "tautulli-4k" {
		t.Fatalf("slug = %q", got)
	}
	if got := alloc("!!!", "radarr"); got != "radarr" {
		t.Fatalf("fallback = %q", got)
	}
	os.MkdirAll(filepath.Join(dir, "radarr"), 0o755)
	if got := alloc("Radarr", "radarr"); got != "radarr-2" {
		t.Fatalf("second radarr = %q, want radarr-2", got)
	}
	os.RemoveAll(filepath.Join(dir, "radarr"))
	if got := alloc("Radarr", "radarr"); got != "radarr" {
		t.Fatalf("after delete = %q, want radarr (name is free again)", got)
	}
}

// A handed-out id is taken until released, then free again if no stack was
// saved under it (the install failed before its Save).
func TestAllocateNameHoldsUntilRelease(t *testing.T) {
	m := NewManager(t.TempDir())
	first, release := m.AllocateName("Radarr", "radarr")
	if second, _ := m.AllocateName("Radarr", "radarr"); second != "radarr-2" || first != "radarr" {
		t.Fatalf("held: %q then %q", first, second)
	}
	release()
	if again, _ := m.AllocateName("Radarr", "radarr"); again != "radarr" {
		t.Errorf("after release = %q, want radarr", again)
	}
}

// Installs at the same time asking for one name each get their own id. The
// name used to be free until the first one's Save, so both got "radarr" and
// the second install wrote over the first.
func TestAllocateNameConcurrentInstallsGetTheirOwnIDs(t *testing.T) {
	m := NewManager(t.TempDir())
	ids := make([]string, 20)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], _ = m.AllocateName("Radarr", "radarr")
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("%q handed out twice: %v", id, ids)
		}
		seen[id] = true
	}
}
