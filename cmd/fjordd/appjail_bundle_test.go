package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/manifest"
)

const testDirector = `services:
  bazarr:
    name: bazarr
    volumes:
      - CONFIG: /config
      - MOVIES_PATH: /movies
      - TV_PATH: /tv
volumes:
  CONFIG:
    device: !ENV '${CONFIG}'
  MOVIES_PATH:
    device: !ENV '${MOVIES_PATH}'
  TV_PATH:
    device: !ENV '${TV_PATH}'
`

// A variable given several folders is rendered as sub-mounts in the compose;
// the director volume must expand to one per folder, not be pruned.
func TestMaterializeDirectorExpandsMultiFolder(t *testing.T) {
	container2host := map[string]string{
		"/config":       "/containers/bazarr/config",
		"/movies/alice": "/mnt/home/alice/movies",
		"/movies/other": "/mnt/other/movies",
		// /tv unresolved -> pruned
	}
	out, placeholders, allVol, err := materializeDirector(testDirector, "7", container2host)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"CONFIG":        "/containers/bazarr/config",
		"MOVIES_PATH_1": "/mnt/home/alice/movies",
		"MOVIES_PATH_2": "/mnt/other/movies",
	} {
		if placeholders[k] != want {
			t.Errorf("%s = %q, want %q", k, placeholders[k], want)
		}
	}
	if _, ok := placeholders["MOVIES_PATH"]; ok {
		t.Error("original MOVIES_PATH should be replaced by its sub-mounts")
	}
	if !allVol["MOVIES_PATH"] || !allVol["TV_PATH"] {
		t.Error("original placeholders must stay in allVol so the .env writer drops them")
	}
	for _, want := range []string{
		"name: 7_bazarr",
		"- MOVIES_PATH_1: /movies/alice",
		"- MOVIES_PATH_2: /movies/other",
		"device: !ENV '${MOVIES_PATH_1}'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("director.yml missing %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"TV_PATH", "- MOVIES_PATH: /movies"} {
		if strings.Contains(out, gone) {
			t.Errorf("director.yml still has %q:\n%s", gone, out)
		}
	}
}

// A sidecar's own jail template travels with the bundle. immich's director
// asks for ${PWD}/immich-postgres-template.conf -- PostgreSQL needs SysV
// shared memory, which the generic template.conf does not grant -- and when
// nothing wrote that file, appjail refused the jail with "Cannot find the
// template" and the stack came up missing its database.
func TestWriteAppjailBundleWritesExtras(t *testing.T) {
	dir := t.TempDir()
	const tmpl = "exec.start: \"/bin/sh /etc/rc\"\nsysvshm: new\n"
	b := &manifest.AppjailBundle{
		Director:     testDirector,
		TemplateConf: "persist\n",
		Extras:       map[string]string{"immich-postgres-template.conf": tmpl},
	}
	if _, err := writeAppjailBundle(dir, "immich", b, map[string]string{}, "services:\n  bazarr:\n    image: x\n", nil, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "immich-postgres-template.conf"))
	if err != nil {
		t.Fatalf("the extra was not written: %v", err)
	}
	if string(got) != tmpl {
		t.Errorf("content = %q, want %q", got, tmpl)
	}
	// The generic template still goes out under its own name -- the two are
	// different files and the director references both.
	if _, err := os.Stat(filepath.Join(dir, "template.conf")); err != nil {
		t.Errorf("template.conf missing alongside the extra: %v", err)
	}
}

// A name that is a path is refused rather than written: these come from a
// catalog manifest, and one escaping the stack dir would land anywhere fjordd
// can write.
func TestWriteAppjailBundleRefusesEscapingExtra(t *testing.T) {
	for _, bad := range []string{"../escape.conf", "/etc/passwd", ".hidden", "sub/dir.conf"} {
		b := &manifest.AppjailBundle{
			Director: testDirector,
			Extras:   map[string]string{bad: "x\n"},
		}
		if _, err := writeAppjailBundle(t.TempDir(), "immich", b, map[string]string{}, "services:\n  bazarr:\n    image: x\n", nil, false); err == nil {
			t.Errorf("accepted extra named %q", bad)
		}
	}
}

// A key the bundle declares survives into .env even with no value, and TZ is
// given the host's zone rather than left blank.
//
// immich's bundle declares TZ= and its director hands ${TZ} to every service.
// Dropping the empty key did not drop the substitution: the jails were started
// with TZ= , and immich-server died at boot on cron's "You specified an invalid
// date", restarting forever.
func TestDirectorEnvKeepsDeclaredKeys(t *testing.T) {
	const defaults = "DIRECTOR_PROJECT=\nDB_USERNAME=\nTZ=\nUNSET_ON_PURPOSE=\n"
	out := directorEnv(defaults, "immich", map[string]string{"DB_USERNAME": "postgres"}, map[string]string{}, map[string]bool{})

	if !strings.Contains(out, "\nUNSET_ON_PURPOSE=\n") {
		t.Errorf("a declared-but-empty key was dropped:\n%s", out)
	}
	if !strings.Contains(out, "DB_USERNAME=postgres\n") {
		t.Errorf("wizard value lost:\n%s", out)
	}
	if !strings.Contains(out, "DIRECTOR_PROJECT=immich\n") {
		t.Errorf("project lost:\n%s", out)
	}
	// TZ is filled from the host when it can be read; on a host with no zone
	// recorded it stays present but empty, which is still better than absent.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "TZ=") {
			if want := hostTimezone(); want != "" && line != "TZ="+want {
				t.Errorf("TZ = %q, want %q", line, "TZ="+want)
			}
			return
		}
	}
	t.Errorf("no TZ line at all:\n%s", out)
}
