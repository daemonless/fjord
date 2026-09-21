package manifest

import (
	"strings"
	"testing"
)

// The real radarr manifest from the embedded catalog.
const radarrManifest = `name: radarr
services:
  radarr:
    image: ghcr.io/daemonless/radarr:latest
    container_name: radarr
    environment:
      - PUID=${PUID}
      - TZ=${TZ}
    volumes:
      - ${CONFIG_DATA}:/config
      - ${MOVIES_PATH}:/movies
    ports:
      - "${WEB_PORT}:7878"
x-fjord:
  version: "0.1"
  variables:
    - name: WEB_PORT
      type: port
      default: "7878"
    - name: CONFIG_DATA
      type: zfs_dataset
      default: "config"
      host_permissions: {uid: 1000, gid: 1000, mode: "755"}
    - name: MOVIES_PATH
      type: path
      default: ""
      optional: true
    - name: TZ
      type: string
      default: "UTC"
    - name: PUID
      type: string
      default: "1000"
`

func TestParseStripsXFjord(t *testing.T) {
	m, err := Parse(radarrManifest)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if strings.Contains(m.Compose(), "x-fjord") {
		t.Fatalf("compose still contains x-fjord:\n%s", m.Compose())
	}
	if !strings.Contains(m.Compose(), "${CONFIG_DATA}:/config") {
		t.Fatalf("compose lost its placeholders:\n%s", m.Compose())
	}
	if strings.Contains(m.Compose(), "container_name") {
		t.Fatalf("container_name not stripped:\n%s", m.Compose())
	}
	if strings.Contains(m.Compose(), "name: radarr") {
		t.Fatalf("top-level project name not stripped:\n%s", m.Compose())
	}
	if len(m.Variables) != 5 {
		t.Fatalf("expected 5 variables, got %d", len(m.Variables))
	}
}

func TestResolveDatasetAndPorts(t *testing.T) {
	m, _ := Parse(radarrManifest)
	res, err := m.Resolve(map[string]string{"WEB_PORT": "7900"}, "radarr", "/containers")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Env["WEB_PORT"] != "7900" {
		t.Fatalf("WEB_PORT override lost: %q", res.Env["WEB_PORT"])
	}
	if res.Env["TZ"] != "UTC" {
		t.Fatalf("TZ default lost: %q", res.Env["TZ"])
	}
	// zfs_dataset -> <base>/<slug>/<name> + a provision dir with 1000:1000/755.
	want := "/containers/radarr/config"
	if res.Env["CONFIG_DATA"] != want {
		t.Fatalf("CONFIG_DATA = %q, want %q", res.Env["CONFIG_DATA"], want)
	}
	if len(res.Dirs) != 1 || res.Dirs[0].Path != want || res.Dirs[0].Uid != 1000 || res.Dirs[0].Mode != 0o755 {
		t.Fatalf("provision dir wrong: %+v", res.Dirs)
	}
	// Empty optional path is flagged, not errored.
	if len(res.EmptyOptional) != 1 || res.EmptyOptional[0] != "MOVIES_PATH" {
		t.Fatalf("MOVIES_PATH should be empty-optional: %+v", res.EmptyOptional)
	}
}

func TestResolveRejectsMissingRequired(t *testing.T) {
	m := &Manifest{Variables: []Var{{Name: "API_KEY", Type: "string"}}}
	if _, err := m.Resolve(map[string]string{}, "x", "/root"); err == nil {
		t.Fatal("expected error for empty required string var")
	}
}

func TestResolveRejectsRelativePath(t *testing.T) {
	m := &Manifest{Variables: []Var{{Name: "DATA", Type: "path"}}}
	if _, err := m.Resolve(map[string]string{"DATA": "relative/dir"}, "x", "/root"); err == nil {
		t.Fatal("expected error for non-absolute path var")
	}
}

func TestResolveRejectsDatasetTraversal(t *testing.T) {
	m := &Manifest{Variables: []Var{{Name: "CONFIG_DATA", Type: "zfs_dataset", Default: "config"}}}
	for _, bad := range []string{"../../../root/x", "a/b", "..", "."} {
		if _, err := m.Resolve(map[string]string{"CONFIG_DATA": bad}, "app", "/containers"); err == nil {
			t.Errorf("Resolve accepted dataset name %q", bad)
		}
	}
	res, err := m.Resolve(map[string]string{"CONFIG_DATA": "config"}, "app", "/containers")
	if err != nil || res.Env["CONFIG_DATA"] != "/containers/app/config" {
		t.Fatalf("plain name: %v %v", err, res)
	}
	res, err = m.Resolve(map[string]string{"CONFIG_DATA": "/mnt/existing"}, "app", "/containers")
	if err != nil || res.Env["CONFIG_DATA"] != "/mnt/existing" {
		t.Fatalf("absolute override: %v %v", err, res)
	}
}

func TestWebContainerPort(t *testing.T) {
	m, err := Parse(radarrManifest)
	if err != nil {
		t.Fatal(err)
	}
	m.WebPort = "${WEB_PORT}"
	if got := m.WebContainerPort(); got != "7878" {
		t.Errorf("container side of ${WEB_PORT}:7878 = %q, want 7878", got)
	}
	m.WebPort = "8443"
	if got := m.WebContainerPort(); got != "8443" {
		t.Errorf("literal = %q, want 8443", got)
	}
	m.WebPort = "${NOPE}"
	if got := m.WebContainerPort(); got != "" {
		t.Errorf("unknown var = %q, want empty", got)
	}
}

// A manifest derived from a variabilized compose can carry "${VAR:-x}" as a
// variable's DEFAULT. Written through to the .env it gave the container an
// environment full of shell syntax -- garage came up with a zone literally
// named ${GARAGE_ZONE:-dc1}. fjord resolves it rather than trusting the
// catalog, because a third-party catalog is not fjord's code.
func TestResolveExpandsReferenceDefaults(t *testing.T) {
	m := &Manifest{Variables: []Var{
		{Name: "GARAGE_ZONE", Default: "${GARAGE_ZONE:-dc1}"},
		{Name: "CAPACITY", Default: "${GARAGE_CAPACITY:-10G}"},
		{Name: "RPC_SECRET", Default: "${RPC_SECRET}", Optional: true},
		{Name: "ADDR", Default: "http://${H:-localhost}:3901"},
		{Name: "PLAIN", Default: "UTC"},
		{Name: "TYPED", Default: "${IGNORED:-no}"},
	}}
	res, err := m.Resolve(map[string]string{"TYPED": "${literally what I typed}"}, "s", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"GARAGE_ZONE": "dc1",
		"CAPACITY":    "10G",
		"RPC_SECRET":  "",
		"ADDR":        "http://localhost:3901",
		"PLAIN":       "UTC",
		// What the operator typed is theirs, reference-looking or not.
		"TYPED": "${literally what I typed}",
	} {
		if got := res.Env[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}
