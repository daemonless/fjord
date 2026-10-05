package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// Changes to one stack's state.json at the same time all land: each loads
// the file after the previous one saved it. Without the lock, writers that
// loaded the same old file each saved their own copy and the last one won.
func TestUpdateStateConcurrentChangesAllLand(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	os.MkdirAll(filepath.Join(dir, "s"), 0o755)
	if err := m.SaveState("s", &State{DesiredState: "running"}); err != nil {
		t.Fatal(err)
	}
	const n = 100
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc := fmt.Sprintf("svc-%03d", i)
			err := m.UpdateState("s", func(st *State) *State {
				st.PendingServices = append(st.PendingServices, svc)
				return st
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	// The setters go through the same lock.
	wg.Add(2)
	go func() { defer wg.Done(); m.SetDisplayName("s", "Immich") }()
	go func() { defer wg.Done(); m.SetGroup("s", "photos") }()
	wg.Wait()

	st, err := m.LoadState("s")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.PendingServices) != n {
		t.Fatalf("%d of %d changes landed", len(st.PendingServices), n)
	}
	for i := range n {
		if !slices.Contains(st.PendingServices, fmt.Sprintf("svc-%03d", i)) {
			t.Errorf("svc-%03d lost", i)
		}
	}
	if st.DisplayName != "Immich" || st.Group != "photos" || st.DesiredState != "running" {
		t.Errorf("a field was lost: %+v", st)
	}
}

// A change that returns nil writes nothing: no state.json appears for a
// stack that had none.
func TestUpdateStateNilWritesNothing(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	os.MkdirAll(filepath.Join(dir, "s"), 0o755)
	if err := m.UpdateState("s", func(st *State) *State { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "s", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("state.json written: %v", err)
	}
}
