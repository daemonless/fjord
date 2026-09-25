package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/daemonless/fjord/pkg/stack"
)

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A root with an app-data location "apps" and three stacks:
//   - notes: its own apps/notes, plus a shared media folder
//   - pair-a: its own apps/pair-a, which pair-b also binds into
//   - linky: apps/linky is a symlink to somewhere else
func deleteFixture(t *testing.T) (*server, string) {
	t.Helper()
	root := t.TempDir()
	apps := filepath.Join(root, "apps")
	media := filepath.Join(root, "media")
	for _, d := range []string{"notes/db", "notes/web", "pair-a/config"} {
		if err := os.MkdirAll(filepath.Join(apps, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(media, 0o755)
	os.MkdirAll(filepath.Join(root, "elsewhere"), 0o755)
	os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(apps, "linky"))
	os.WriteFile(filepath.Join(apps, "notes/db/ibdata1"), make([]byte, 1000), 0o644)
	writeFile(t, filepath.Join(root, "settings.json"), `{"appData":["`+apps+`"]}`)

	addStack := func(name, compose string) {
		writeFile(t, filepath.Join(root, "stacks", name, "compose.yaml"), compose)
	}
	addStack("notes", "services:\n  db:\n    image: x\n    volumes:\n      - "+apps+"/notes/db:/config\n      - "+media+":/media:ro\n      - cache:/cache\n  web:\n    image: x\n    volumes:\n      - ${DATA}/web:/config\nvolumes:\n  cache:\n")
	writeFile(t, filepath.Join(root, "stacks/notes/.env"), "DATA="+apps+"/notes\n")
	addStack("pair-a", "services:\n  a:\n    image: x\n    volumes:\n      - "+apps+"/pair-a/config:/config\n")
	addStack("pair-b", "services:\n  b:\n    image: x\n    volumes:\n      - "+apps+"/pair-a/config:/shared:ro\n")
	addStack("linky", "services:\n  l:\n    image: x\n    volumes:\n      - "+apps+"/linky:/config\n")
	return &server{manager: stack.NewManager(filepath.Join(root, "stacks")), fjordRoot: root}, root
}

func plan(t *testing.T, s *server, name string) deletePlan {
	t.Helper()
	st, err := s.manager.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return s.planDelete(context.Background(), st)
}

// Its own folder -- found through a ${VAR} too -- is offered; the shared
// media folder and the named volume are only listed as kept.
func TestDeletePlanOwnData(t *testing.T) {
	s, root := deleteFixture(t)
	p := plan(t, s, "notes")
	if len(p.AppData) != 1 || p.AppData[0].Path != filepath.Join(root, "apps/notes") || p.AppData[0].Bytes != 1000 {
		t.Fatalf("app data: %+v", p.AppData)
	}
	kept := map[string]string{}
	for _, k := range p.Keeps {
		kept[k.Path] = k.Why
	}
	if kept[filepath.Join(root, "media")] == "" || kept["cache"] == "" || len(kept) != 2 {
		t.Errorf("keeps: %+v", p.Keeps)
	}
}

// A folder another stack uses is never this stack's to delete.
func TestDeletePlanSharedFolder(t *testing.T) {
	s, _ := deleteFixture(t)
	if p := plan(t, s, "pair-a"); len(p.AppData) != 0 {
		t.Errorf("pair-a offered a folder pair-b uses: %+v", p.AppData)
	}
}

// Removing through a symlink would delete whatever it points at.
func TestDeletePlanSymlink(t *testing.T) {
	s, _ := deleteFixture(t)
	if p := plan(t, s, "linky"); len(p.AppData) != 0 {
		t.Errorf("a symlinked folder was offered: %+v", p.AppData)
	}
}

// Delete with data removes exactly the planned folders, then the stack;
// without it, the data stays.
func TestDeleteWithData(t *testing.T) {
	s, root := deleteFixture(t)
	rec := httptest.NewRecorder()
	s.stackDelete(rec, "notes", false)
	if rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "apps/notes/db/ibdata1")); err != nil {
		t.Errorf("plain delete removed app data")
	}

	s, root = deleteFixture(t)
	rec = httptest.NewRecorder()
	s.stackDelete(rec, "notes", true)
	if rec.Code != 200 {
		t.Fatalf("delete with data: %d %s", rec.Code, rec.Body)
	}
	for _, gone := range []string{"apps/notes", "stacks/notes"} {
		if _, err := os.Stat(filepath.Join(root, gone)); err == nil {
			t.Errorf("%s still there", gone)
		}
	}
	for _, stays := range []string{"media", "apps/pair-a/config", "elsewhere"} {
		if _, err := os.Stat(filepath.Join(root, stays)); err != nil {
			t.Errorf("%s was removed", stays)
		}
	}
}
