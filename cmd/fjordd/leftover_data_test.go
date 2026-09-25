package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
)

// mountingEngine is an engine whose containers mount the given host paths --
// one fjord does not manage, as on jupiter.
type mountingEngine struct {
	engine.Backend
	paths []string
}

func (m mountingEngine) HostMounts(context.Context) ([]string, error) { return m.paths, nil }

// The delete fixture, plus: a folder nothing uses (a deleted stack's), one a
// non-fjord container mounts, and a hidden one.
func leftoverFixture(t *testing.T) (*server, string) {
	s, root := deleteFixture(t)
	apps := filepath.Join(root, "apps")
	for _, d := range []string{"oldapp/config", "ansible-svc", ".zfs"} {
		os.MkdirAll(filepath.Join(apps, d), 0o755)
	}
	os.WriteFile(filepath.Join(apps, "oldapp/config/db"), make([]byte, 500), 0o644)
	s.backends = map[string]engine.Backend{
		"podman": mountingEngine{engine.Unavailable("podman"), []string{filepath.Join(apps, "ansible-svc")}},
	}
	return s, root
}

func TestLeftoversOnlyUnusedFolders(t *testing.T) {
	s, root := leftoverFixture(t)
	found, err := s.findLeftovers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Folders) != 1 || found.Folders[0].Path != filepath.Join(root, "apps/oldapp") || found.Folders[0].Bytes != 500 {
		t.Fatalf("want only apps/oldapp, got %+v", found.Folders)
	}
	if len(found.Unchecked) != 0 {
		t.Errorf("unchecked: %v", found.Unchecked)
	}
}

// An engine that cannot list its containers' mounts is named, so the page
// can say a folder may still be in use.
func TestLeftoversNameUncheckedEngines(t *testing.T) {
	s, _ := leftoverFixture(t)
	s.backends["appjail"] = engine.Unavailable("appjail")
	found, _ := s.findLeftovers(context.Background())
	if len(found.Unchecked) != 1 || found.Unchecked[0] != "appjail" {
		t.Errorf("unchecked: %v", found.Unchecked)
	}
}

// Only a folder on the list, as worked out now, can be removed.
func TestLeftoverRemove(t *testing.T) {
	s, root := leftoverFixture(t)
	post := func(path string) int {
		rec := httptest.NewRecorder()
		s.handleLeftovers(rec, httptest.NewRequest("POST", "/api/maintenance/leftovers", strings.NewReader(`{"path":"`+path+`"}`)))
		return rec.Code
	}
	for _, refused := range []string{"apps/ansible-svc", "apps/notes", "apps/linky", "media", ".."} {
		if code := post(filepath.Join(root, refused)); code != 409 {
			t.Errorf("%s: %d, want 409", refused, code)
		}
	}
	if code := post(filepath.Join(root, "apps/oldapp")); code != 204 {
		t.Fatalf("oldapp: %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, "apps/oldapp")); err == nil {
		t.Error("oldapp still there")
	}
	for _, stays := range []string{"apps/ansible-svc", "apps/notes", "elsewhere", "media"} {
		if _, err := os.Stat(filepath.Join(root, stays)); err != nil {
			t.Errorf("%s was removed", stays)
		}
	}
}
