package main

import (
	"strings"
	"testing"
)

// A director stack as an install writes it: !ENV devices, two services.
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

// Unmount takes one mount of one service: the entry goes, a device nobody
// mounts any more goes, one still mounted elsewhere stays. The !ENV tags the
// install wrote survive the round trip.
func TestDirectorRemoveMountTakesOneEntry(t *testing.T) {
	out, err := directorRemoveMount(directorWithMounts, "app", "/books")
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

	// /config is on both services: taking it off db keeps the device for app.
	out, err = directorRemoveMount(directorWithMounts, "db", "/config")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "APP_CONFIG: /config") != 1 || !strings.Contains(out, "device: !ENV '${APP_CONFIG}'") {
		t.Errorf("app's /config and the shared device must stay:\n%s", out)
	}
	if _, err := directorRemoveMount(directorWithMounts, "app", "/nothing"); err == nil {
		t.Error("removing what is not mounted should be an error")
	}
	if _, err := directorRemoveMount(directorWithMounts, "nope", "/config"); err == nil {
		t.Error("a service the file does not have should be an error")
	}
}

func TestDirectorAttachBindMount(t *testing.T) {
	out, err := directorAttachBindMount(directorWithMounts, "app", "/tank/media", "/media/books", []string{"ro"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- MEDIA_BOOKS: /media/books", "MEDIA_BOOKS:\n    device: /tank/media\n    options: ro"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	if _, err := directorAttachBindMount(directorWithMounts, "app", "/tank/x", "/books", nil); err == nil {
		t.Error("a path already mounted should be refused")
	}
	if _, err := directorAttachBindMount(directorWithMounts, "app", "/tank/x", "/x", []string{"z"}); err == nil || !strings.Contains(err.Error(), "podman") {
		t.Errorf("a podman-only option should be refused by name, got %v", err)
	}
	if _, err := directorAttachBindMount(directorWithMounts, "app", "tank/x", "/x", nil); err == nil {
		t.Error("a relative host path should be refused")
	}

	// A service with no volumes yet, in a file with no project volumes.
	out, err = directorAttachBindMount("services:\n  web:\n    name: w\n", "web", "/srv/www", "/usr/local/www", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "volumes:\n      - USR_LOCAL_WWW: /usr/local/www") || !strings.Contains(out, "volumes:\n  USR_LOCAL_WWW:\n    device: /srv/www") {
		t.Errorf("both halves of the mount must be written:\n%s", out)
	}
	// A name already taken gets a suffix rather than overwriting the device.
	out, _ = directorAttachBindMount(out, "web", "/other", "/usr/local/www/", nil)
	if !strings.Contains(out, "USR_LOCAL_WWW_2") {
		t.Errorf("a second mount with the same name stem needs its own device:\n%s", out)
	}
}

func TestDirectorSetMountOptions(t *testing.T) {
	out, err := directorSetMountOptions(directorWithMounts, "app", "/books", []string{"ro"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "APP_BOOKS:\n    device: !ENV '${APP_BOOKS}'\n    options: ro") {
		t.Errorf("read-only goes on the project volume:\n%s", out)
	}
	back, err := directorSetMountOptions(out, "app", "/books", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(back, "options: ro") {
		t.Errorf("clearing the option must drop it:\n%s", back)
	}
	if _, err := directorSetMountOptions(directorWithMounts, "app", "/none", []string{"ro"}); err == nil {
		t.Error("options on nothing mounted should be an error")
	}
}
