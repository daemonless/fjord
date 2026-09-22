package compose

import (
	"strings"
	"testing"
)

const volBase = `services:
  app:
    image: example.org/app:latest
    ports:
      - "8080:8080"
`

func TestAttachVolume(t *testing.T) {
	out, err := AttachVolume(volBase, FirstService, "media", "/data", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"media:/data:ro"`, "external: true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if got := NamedVolumes(out); len(got) != 1 || got[0] != "media" {
		t.Fatalf("NamedVolumes = %v, want [media]", got)
	}
	// attaching the same volume again is rejected
	if _, err := AttachVolume(out, FirstService, "media", "/other", false); err == nil {
		t.Fatal("expected duplicate-attach error")
	}
}

func TestAttachVolumeValidation(t *testing.T) {
	if _, err := AttachVolume(volBase, FirstService, "", "/data", false); err == nil {
		t.Fatal("expected error for empty name")
	}
	if _, err := AttachVolume(volBase, FirstService, "media", "data", false); err == nil {
		t.Fatal("expected error for relative path")
	}
}

// A stack of several services is the case the first-service pick could not
// describe: the mount goes where the caller said, not where the file happens
// to list first.
const multiBase = `services:
  app:
    image: example.org/app:latest
    volumes:
      - /etc/localtime:/etc/localtime:ro
  database:
    image: example.org/db:latest
    volumes:
      - /etc/localtime:/etc/localtime:ro
      - dbdata:/var/lib/db
`

func TestAttachBindMountNamesItsService(t *testing.T) {
	out, err := AttachBindMount(multiBase, "database", "/mnt", "/test", false)
	if err != nil {
		t.Fatal(err)
	}
	vols := volumesOfService(t, out, "database")
	if !containsEntry(vols, "/mnt:/test") {
		t.Errorf("database did not get the mount: %v", vols)
	}
	if app := volumesOfService(t, out, "app"); containsEntry(app, "/mnt:/test") {
		t.Errorf("app got a mount meant for database: %v", app)
	}
}

func TestAttachBindMountUnknownService(t *testing.T) {
	if _, err := AttachBindMount(multiBase, "nope", "/mnt", "/test", false); err == nil {
		t.Fatal("expected an error naming a service that is not there")
	}
}

// Unmounting a path one service holds must not unmount the sibling that holds
// the same path. /etc/localtime is on four of immich's services.
func TestRemoveMountLeavesOtherServicesAlone(t *testing.T) {
	out, err := RemoveMount(multiBase, "app", "/etc/localtime")
	if err != nil {
		t.Fatal(err)
	}
	if app := volumesOfService(t, out, "app"); containsEntry(app, "/etc/localtime:/etc/localtime:ro") {
		t.Errorf("app still holds the mount: %v", app)
	}
	db := volumesOfService(t, out, "database")
	if !containsEntry(db, "/etc/localtime:/etc/localtime:ro") {
		t.Errorf("database lost a mount it was not asked about: %v", db)
	}
	if !containsEntry(db, "dbdata:/var/lib/db") {
		t.Errorf("database lost an unrelated mount: %v", db)
	}
}

func volumesOfService(t *testing.T, composeYAML, service string) []VolMount {
	t.Helper()
	for _, s := range ParseServices(composeYAML, nil) {
		if s.Name == service {
			return s.Volumes
		}
	}
	t.Fatalf("no service %q in:\n%s", service, composeYAML)
	return nil
}

// containsEntry matches "<source-or-name>:<dest>" as the compose file writes it.
func containsEntry(vols []VolMount, want string) bool {
	for _, v := range vols {
		src := v.Source
		if src == "" {
			src = v.Name
		}
		got := src + ":" + v.Dest
		if v.ReadOnly {
			got += ":ro"
		}
		if got == want {
			return true
		}
	}
	return false
}
