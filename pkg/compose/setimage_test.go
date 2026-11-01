package compose

import (
	"strings"
	"testing"
)

// Rollback pins ONE service; the rest of a multi-image stack must come back
// byte-for-byte what the operator wrote, ${VAR}s and comments included.
func TestSetServiceImage(t *testing.T) {
	in := `services:
  immich-server:
    # the app
    image: ghcr.io/x/immich-server:${IMMICH_TAG:-latest}
  database:
    image: ghcr.io/x/immich-postgres:latest
`
	out, err := SetServiceImage(in, "database", "ghcr.io/x/immich-postgres:latest@sha256:abc")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"image: ghcr.io/x/immich-postgres:latest@sha256:abc",
		"image: ghcr.io/x/immich-server:${IMMICH_TAG:-latest}",
		"# the app",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if _, err := SetServiceImage(in, "nope", "x"); err == nil {
		t.Error("unknown service accepted")
	}
}

// A multi-image stack takes a new version one service at a time: the db
// stays on its own tag, and a pin on the moved service goes with the old tag.
func TestSetServiceTag(t *testing.T) {
	in := `services:
  db:
    image: ghcr.io/x/mariadb:10.11
  web:
    image: ghcr.io/x/zensical:0.0.63@sha256:abc
`
	out, err := SetServiceTag(in, "web", "0.0.64")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "image: ghcr.io/x/zensical:0.0.64\n") || !strings.Contains(out, "image: ghcr.io/x/mariadb:10.11") {
		t.Errorf("got:\n%s", out)
	}
	if _, err := SetServiceTag(in, "nope", "1"); err == nil {
		t.Error("unknown service accepted")
	}
}

// A tag written as a variable is not a tag to rewrite: cutting at the last
// colon produced "${IMMICH_TAG:3.2.2" (immich on netlab, 2026-09-30). The
// variable is named for the caller to set; the reference is left alone.
func TestImageTagVar(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/x/immich-server:${IMMICH_TAG:-latest}":           "IMMICH_TAG",
		"ghcr.io/x/immich-server:${IMMICH_TAG}":                   "IMMICH_TAG",
		"ghcr.io/x/immich-server:${IMMICH_TAG-latest}@sha256:abc": "IMMICH_TAG",
		"ghcr.io/x/immich-server:latest":                          "",
		"registry:5000/app":                                       "",
		"ghcr.io/x/app":                                           "",
	}
	for image, want := range cases {
		got, ok := ImageTagVar(image)
		if got != want || ok != (want != "") {
			t.Errorf("ImageTagVar(%q) = %q,%v want %q", image, got, ok, want)
		}
	}
	in := "services:\n  a:\n    image: ghcr.io/x/immich-server:${IMMICH_TAG:-latest}\n"
	out, err := SetServiceTag(in, "a", "3.2.2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "image: ghcr.io/x/immich-server:${IMMICH_TAG:-latest}") {
		t.Errorf("a variable tag was rewritten:\n%s", out)
	}
	if out, _ := SetImageTag(in, "3.2.2"); !strings.Contains(out, "${IMMICH_TAG:-latest}") {
		t.Errorf("SetImageTag rewrote a variable tag:\n%s", out)
	}
}

// The variant picked at install is the app's: a database a choice added,
// pinned to mariadb:11.4, came up as mariadb:latest (MariaDB 12.3; uptime-kuma
// on netlab, 2026-10-11). Only the app's repo moves, every service of it.
func TestSetRepoTag(t *testing.T) {
	in := `services:
  app:
    image: ghcr.io/x/app:latest
  agent:
    image: ghcr.io/x/app:latest@sha256:abc
  db:
    image: ghcr.io/x/mariadb:11.4
  cache:
    image: ghcr.io/x/redis
`
	out, err := SetRepoTag(in, "ghcr.io/x/app", "pkg")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"image: ghcr.io/x/app:pkg\n",
		"image: ghcr.io/x/mariadb:11.4\n",
		"image: ghcr.io/x/redis\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "ghcr.io/x/app:pkg") != 2 {
		t.Errorf("both services of the app's image move:\n%s", out)
	}
	// No repo known: every image, as SetImageTag always did.
	all, _ := SetRepoTag(in, "", "pkg")
	if !strings.Contains(all, "ghcr.io/x/mariadb:pkg") {
		t.Errorf("repo \"\" left the db alone:\n%s", all)
	}
}
