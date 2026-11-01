package main

import (
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/manifest"
)

const choiceDirector = `options:
  - alias:
  - ip4_inherit:
services:
  immich-server:
    name: immich_server
    options:
      - from: ghcr.io/daemonless/immich-server:latest
    volumes:
      - upload: /data
  immich-machine-learning:
    name: immich_machine_learning
    options:
      - from: ghcr.io/daemonless/immich-ml:latest
    volumes:
      - model-cache: /cache
volumes:
  upload:
    device: !ENV '${UPLOAD_LOCATION}'
  model-cache:
    device: !ENV '${CACHE_LOCATION}'
`

// dbuild's form for Vikunja's MariaDB option (2026-10-05), its jail on top.
const mariadbForm = `services:
  vikunja-mariadb:
    name: vikunja_mariadb
    priority: 10
    options:
    - from: ghcr.io/daemonless/mariadb:11.4
    - template: !ENV '${PWD}/template.conf'
    oci:
      environment:
      - MYSQL_USER: !ENV '${VIKUNJA_DATABASE_USER}'
      - MYSQL_PASSWORD: !ENV '${VIKUNJA_DATABASE_PASSWORD}'
    volumes:
    - database: /config
volumes:
  database:
    device: !ENV '${DATABASE_LOCATION}'
`

func TestWithAppjailChoices(t *testing.T) {
	b := &manifest.AppjailBundle{Director: choiceDirector, EnvDefaults: "UPLOAD_LOCATION=\nCACHE_LOCATION=\n"}
	picked := []*manifest.Option{
		{ID: "off", Drop: []string{"immich-machine-learning"}},
		{ID: "mariadb", Appjail: &manifest.OptionAppjail{
			Director:  mariadbForm,
			Files:     map[string]string{"mariadb-template.conf": "exec.start = \"/bin/sh /etc/rc\";"},
			Hostnames: map[string]string{"vikunja-mariadb": "VIKUNJA_DATABASE_HOST"},
		}},
	}
	got, err := withAppjailChoices(b, picked)
	if err != nil {
		t.Fatal(err)
	}
	d := got.Director
	if strings.Contains(d, "immich-machine-learning") {
		t.Error("the dropped service is still a jail")
	}
	if strings.Contains(d, "model-cache") {
		t.Error("a volume only the dropped service mounted is still declared")
	}
	for _, want := range []string{"vikunja-mariadb:", "from: ghcr.io/daemonless/mariadb:11.4", "device: !ENV '${DATABASE_LOCATION}'", "immich-server:", "upload:"} {
		if !strings.Contains(d, want) {
			t.Errorf("director lacks %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, `'!ENV`) {
		t.Errorf("!ENV became a quoted string:\n%s", d)
	}
	if got.Extras["mariadb-template.conf"] == "" {
		t.Error("the form's file is not in the bundle")
	}
	for _, v := range []string{"VIKUNJA_DATABASE_USER=", "VIKUNJA_DATABASE_PASSWORD=", "DATABASE_LOCATION="} {
		if !strings.Contains(got.EnvDefaults, v) {
			t.Errorf(".env does not declare %s: %q", v, got.EnvDefaults)
		}
	}
	if strings.Contains(got.EnvDefaults, "PWD=") {
		t.Error("PWD is the shell's, not .env's")
	}
	if b.Director != choiceDirector || len(b.Extras) != 0 {
		t.Error("the catalog's bundle was changed in place")
	}
	if h := appjailHostnames("t-vik", picked); h["VIKUNJA_DATABASE_HOST"] != directorJailName("t-vik", "vikunja-mariadb") {
		t.Errorf("host variable: %v", h)
	}
}

// Defaults all round: the bundle is used as it is.
func TestWithAppjailChoicesNothingPicked(t *testing.T) {
	b := &manifest.AppjailBundle{Director: choiceDirector}
	got, err := withAppjailChoices(b, []*manifest.Option{{ID: "on"}})
	if err != nil || got != b {
		t.Fatalf("got %v, %v", got, err)
	}
}

// LibreNMS: two services in the compose, one jail in the bundle. Refused
// before anything is saved, naming the service with no jail.
func TestAppjailShort(t *testing.T) {
	compose := "services:\n  librenms:\n    image: a\n  librenms-mariadb:\n    image: b\n"
	one := &manifest.AppjailBundle{Director: "services:\n  librenms:\n    name: librenms\n"}
	if got := appjailShort(one, nil, compose); len(got) != 1 || got[0] != "librenms-mariadb" {
		t.Fatalf("one jail for two services: %v", got)
	}
	// One jail per service, named differently (a choice's database is
	// "mariadb" in the compose, "vikunja-mariadb" in the director): fine.
	two := &manifest.AppjailBundle{Director: "services:\n  librenms:\n    name: a\n  librenms-db:\n    name: b\n"}
	if got := appjailShort(two, nil, compose); got != nil {
		t.Fatalf("one jail per service: %v", got)
	}
}
