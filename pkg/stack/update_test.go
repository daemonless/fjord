package stack

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Changes to one stack at the same time all land, and a Save in the middle
// is not interleaved with any of them: each Update reads the files after the
// previous write. Get then Save lost all but the last.
func TestUpdateConcurrentChangesAllLand(t *testing.T) {
	m := NewManager(t.TempDir())
	if err := m.Save(&Stack{Name: "s", Compose: "services: {}\n", Env: "A=1\n"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Update("s", func(st *Stack) error {
				st.Compose += fmt.Sprintf("# %d\n", i)
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	st, _ := m.Get("s")
	for i := range 20 {
		if !strings.Contains(st.Compose, fmt.Sprintf("# %d\n", i)) {
			t.Errorf("change %d lost:\n%s", i, st.Compose)
		}
	}
	if st.Env != "A=1\n" {
		t.Errorf("env %q", st.Env)
	}
}

// A refused change writes nothing, and its error comes back as is.
func TestUpdateErrorWritesNothing(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Save(&Stack{Name: "s", Compose: "services: {}\n"})
	refused := errors.New("no")
	_, err := m.Update("s", func(st *Stack) error {
		st.Compose = "changed\n"
		return refused
	})
	if err != refused {
		t.Fatalf("err %v", err)
	}
	if st, _ := m.Get("s"); st.Compose != "services: {}\n" {
		t.Errorf("written anyway: %q", st.Compose)
	}
}

// keepAddresses runs on every start: with nothing to pin the files are left
// alone, not rewritten.
func TestUpdateUnchangedDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	m.Save(&Stack{Name: "s", Compose: "services: {}\n"})
	path := filepath.Join(dir, "s", "compose.yaml")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)
	if _, err := m.Update("s", func(*Stack) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); !fi.ModTime().Equal(old) {
		t.Errorf("rewritten: mtime %v", fi.ModTime())
	}
}

// An Update after a Delete finds no stack; it does not bring the directory
// back the way a Save of a copy loaded before the delete did.
func TestUpdateAfterDeleteIsNotFound(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	m.Save(&Stack{Name: "s", Compose: "services: {}\n"})
	if err := m.Delete("s"); err != nil {
		t.Fatal(err)
	}
	_, err := m.Update("s", func(st *Stack) error {
		st.Compose = "back\n"
		return nil
	})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "s")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stack dir came back: %v", err)
	}
}

// Once a delete has begun, Replace writes nothing; after it, Replace finds
// no stack and the directory stays gone. Save brought the stack back.
func TestReplaceRefusesADeletedStack(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	m.Save(&Stack{Name: "s", Compose: "services: {}\n"})
	m.SetDesiredState("s", "deleting")
	if err := m.Replace(&Stack{Name: "s", Compose: "changed\n"}, nil); !errors.Is(err, ErrDeleting) {
		t.Errorf("deleting: err %v", err)
	}
	if st, _ := m.Get("s"); st.Compose != "services: {}\n" {
		t.Errorf("written while deleting: %q", st.Compose)
	}
	if err := m.Delete("s"); err != nil {
		t.Fatal(err)
	}
	if err := m.Replace(&Stack{Name: "s", Compose: "back\n"}, nil); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("deleted: err %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "s")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stack dir came back: %v", err)
	}
}

// A stack that is not being deleted is replaced as Save would.
func TestReplaceWrites(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Save(&Stack{Name: "s", Compose: "services: {}\n"})
	m.SetDesiredState("s", "running")
	if err := m.Replace(&Stack{Name: "s", Compose: "changed\n"}, nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := m.Get("s"); st.Compose != "changed\n" {
		t.Errorf("compose %q", st.Compose)
	}
}

// Replace's check sees the stack as it is on disk when it writes, under the
// lock Update takes: a change made after the caller read the stack (a
// rollback's pin) is caught there, and the refused replace writes nothing.
func TestReplaceCheckSeesTheStackOnDisk(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Save(&Stack{Name: "s", Compose: "image: a\n"})
	read, _ := m.Get("s")                                                              // the editor's save reads the stack...
	m.Update("s", func(st *Stack) error { st.Compose = "image: a@pin\n"; return nil }) // ...a rollback pins it
	stale := errors.New("stale")
	read.Compose = "image: a\nedited: true\n"
	err := m.Replace(read, func(cur *Stack) error {
		if cur.Compose != "image: a\n" {
			return stale
		}
		return nil
	})
	if err != stale {
		t.Fatalf("err %v, want the check's", err)
	}
	if st, _ := m.Get("s"); st.Compose != "image: a@pin\n" {
		t.Errorf("pin written over: %q", st.Compose)
	}
}
