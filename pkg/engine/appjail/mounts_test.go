package appjail

import (
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
)

const directorWithMounts = `options:
  - virtualnet: ':<random> default'
services:
  app:
    name: t_abs_app
    options:
      - expose: !ENV '${WEB_PORT}:13378 proto:tcp'
    volumes:
      - APP_CONFIG: /config
      - APP_BOOKS: /books
  db:
    name: t_abs_db
    volumes:
      - APP_CONFIG: /config
volumes:
  APP_CONFIG:
    device: !ENV '${APP_CONFIG}'
  APP_BOOKS:
    device: !ENV '${APP_BOOKS}'
`

func TestRemoveMount(t *testing.T) {
	out, err := RemoveMount(directorWithMounts, "app", "/books")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "/books") || strings.Contains(out, "APP_BOOKS") {
		t.Errorf("the /books mount and its unused device should be gone:\n%s", out)
	}
	if !strings.Contains(out, "- APP_CONFIG: /config") || !strings.Contains(out, "device: !ENV '${APP_CONFIG}'") {
		t.Errorf("the other mount, its device and its !ENV tag must stay:\n%s", out)
	}
	if !strings.Contains(out, "expose: !ENV '${WEB_PORT}:13378 proto:tcp'") {
		t.Errorf("unrelated !ENV options must survive:\n%s", out)
	}

	out, err = RemoveMount(directorWithMounts, "db", "/config")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "APP_CONFIG: /config") != 1 || !strings.Contains(out, "device: !ENV '${APP_CONFIG}'") {
		t.Errorf("app's /config and the shared device must stay:\n%s", out)
	}
	if _, err := RemoveMount(directorWithMounts, "app", "/nothing"); err == nil {
		t.Error("removing what is not mounted should be an error")
	}
	if _, err := RemoveMount(directorWithMounts, "nope", "/config"); err == nil {
		t.Error("a service the file does not have should be an error")
	}
}

func TestAttachBindMount(t *testing.T) {
	out, err := AttachBindMount(directorWithMounts, "app", "/tank/media", "/media/books", []string{"ro"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- MEDIA_BOOKS: /media/books", "MEDIA_BOOKS:\n    device: /tank/media\n    options: ro"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	if _, err := AttachBindMount(directorWithMounts, "app", "/tank/x", "/books", nil); err == nil {
		t.Error("a path already mounted should be refused")
	}
	if _, err := AttachBindMount(directorWithMounts, "app", "/tank/x", "/x", []string{"z"}); err == nil || !strings.Contains(err.Error(), "podman") {
		t.Errorf("a podman-only option should be refused by name, got %v", err)
	}
	if _, err := AttachBindMount(directorWithMounts, "app", "tank/x", "/x", nil); err == nil {
		t.Error("a relative host path should be refused")
	}
}

func TestSetMountOptions(t *testing.T) {
	out, err := SetMountOptions(directorWithMounts, "app", "/books", []string{"ro"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "APP_BOOKS:\n    device: !ENV '${APP_BOOKS}'\n    options: ro") {
		t.Errorf("read-only goes on the project volume:\n%s", out)
	}
	back, err := SetMountOptions(out, "app", "/books", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(back, "options: ro") {
		t.Errorf("clearing the option must drop it:\n%s", back)
	}
	if _, err := SetMountOptions(directorWithMounts, "app", "/none", []string{"ro"}); err == nil {
		t.Error("options on nothing mounted should be an error")
	}
}

func TestEngineSpecMountEditor(t *testing.T) {
	var ed engine.SpecMountEditor = NewBackend()
	added, err := ed.AttachSpecBindMount(directorWithMounts, "app", "/tank/music", "/music", []string{"ro"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(added, "- MUSIC: /music") {
		t.Fatalf("missing attached mount:\n%s", added)
	}
	rw, err := ed.SetSpecMountOptions(added, "app", "/music", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rw, "options: ro") {
		t.Fatalf("expected ro option cleared:\n%s", rw)
	}
	removed, err := ed.RemoveSpecMount(rw, "app", "/music")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(removed, "/music") {
		t.Fatalf("expected /music removed:\n%s", removed)
	}
}
