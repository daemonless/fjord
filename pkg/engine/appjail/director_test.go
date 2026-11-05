package appjail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/stack"
)

func TestParseExpose(t *testing.T) {
	cases := map[string]struct {
		host, cont int
		proto      string
	}{
		"17878:7878 proto:tcp": {17878, 7878, "tcp"},
		"53:53 proto:udp":      {53, 53, "udp"},
		"8080":                 {8080, 8080, "tcp"},
	}
	for spec, want := range cases {
		p, ok := parseExpose(spec)
		if !ok || p.Host != want.host || p.Container != want.cont || p.Proto != want.proto {
			t.Errorf("%q -> %+v ok=%v, want %+v", spec, p, ok, want)
		}
	}
	if _, ok := parseExpose("${WEB_PORT}:7878"); ok {
		t.Error("unresolved variable must not parse")
	}
}

// A director stack's jail names come from the spec's `name:` (as the user may
// have edited it), with ports resolved from .env -- not from compose.yaml.
func TestDirectorServicesUsesSpecNames(t *testing.T) {
	dir := t.TempDir()
	spec := "services:\n  radarr:\n    name: 104radarr\n    options:\n      - expose: !ENV '${WEB_PORT}:7878 proto:tcp'\n"
	os.WriteFile(filepath.Join(dir, "appjail-director.yml"), []byte(spec), 0o644)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("WEB_PORT=17878\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  radarr:\n    image: x\n"), 0o644)

	b := NewBackend()
	sj := b.serviceJails(&stack.Stack{Name: "104", Dir: dir})
	if len(sj) != 1 || sj[0].jail != "104radarr" || sj[0].svc.Name != "radarr" {
		t.Fatalf("unexpected services: %+v", sj)
	}
	if len(sj[0].svc.Ports) != 1 || sj[0].svc.Ports[0].Host != 17878 || sj[0].svc.Ports[0].Container != 7878 {
		t.Errorf("ports not resolved from .env: %+v", sj[0].svc.Ports)
	}
}

func TestDirectorEnvPinsPWDAndHome(t *testing.T) {
	t.Setenv("PWD", "/somewhere/else")
	t.Setenv("HOME", "/")
	var pwd, home string
	for _, kv := range directorEnv("/var/db/fjord/stacks/104") {
		if v, ok := strings.CutPrefix(kv, "PWD="); ok {
			pwd = v
		}
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = v
		}
	}
	if pwd != "/var/db/fjord/stacks/104" {
		t.Fatalf("PWD = %q, want the stack dir", pwd)
	}
	if home == "/" || home == "" {
		t.Fatalf("HOME = %q, want the user's real home", home)
	}
}

// A per-service update takes only the jails asked for, and refuses a name the
// spec does not have rather than rebuilding nothing and calling it done.
func TestJailsOf(t *testing.T) {
	all := []svcJail{
		{svc: composepkg.Service{Name: "app"}, jail: "s_app"},
		{svc: composepkg.Service{Name: "db"}, jail: "s_db"},
	}
	got, err := jailsOf(all, []string{"db"})
	if err != nil || len(got) != 1 || got[0].jail != "s_db" {
		t.Fatalf("db -> %+v, %v", got, err)
	}
	if _, err := jailsOf(all, []string{"db", "cache"}); err == nil || !strings.Contains(err.Error(), `"cache"`) {
		t.Fatalf("an unknown service must be refused by name, got %v", err)
	}
}

// A stack with no .env still gets one director project, named after it; a
// .env that names the project has the say.
func TestDirectorEnvNamesTheProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "t-native")
	os.MkdirAll(dir, 0o755)
	has := func(want string) bool {
		for _, kv := range directorEnv(dir) {
			if kv == want {
				return true
			}
		}
		return false
	}
	if !has("DIRECTOR_PROJECT=t-native") {
		t.Error("no .env: the project must be the stack's name")
	}
	os.WriteFile(filepath.Join(dir, ".env"), []byte("DIRECTOR_PROJECT=mine\n"), 0o600)
	if has("DIRECTOR_PROJECT=t-native") {
		t.Error(".env names the project: the environment must not override it")
	}
}

// A jail on its own LAN address has its ports stashed by install, and the
// health probe needs one: without it authelia read "crashed" while serving,
// because its pre-redis crash was still in the log tail.
func TestComposePortsForUsesTheStash(t *testing.T) {
	s := &stack.Stack{Compose: "services:\n  authelia-server:\n    image: x\n    x-fjord-published:\n      - 9091:9091\n  redis:\n    image: y\n"}
	got := composePortsFor(s, "authelia-server", nil)
	if len(got) != 1 || got[0].Container != 9091 {
		t.Fatalf("authelia-server ports = %+v, want the stashed 9091", got)
	}
	if got := composePortsFor(s, "redis", nil); len(got) != 0 {
		t.Fatalf("redis has none, got %+v", got)
	}
	s.Compose = "services:\n  app:\n    image: x\n    ports:\n      - 8080:80\n    x-fjord-published:\n      - 9999:9999\n"
	if got := composePortsFor(s, "app", nil); len(got) != 1 || got[0].Container != 80 {
		t.Fatalf("published ports win over a stale stash, got %+v", got)
	}
}

// Destroying a jail removes its log directory under /var/log/appjail/jails so
// a new jail created with the same name does not inherit the old jail's log.
func TestRemoveJailLogsClearsDeletedJailLog(t *testing.T) {
	old := jailLogRoot
	jailLogRoot = t.TempDir()
	defer func() { jailLogRoot = old }()

	logDir := filepath.Join(jailLogRoot, "radarr", "container")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "container.log"), []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := latestLog("radarr"); got == "" {
		t.Fatal("expected latestLog to find log before removal")
	}
	removeJailLogs("radarr")
	if got := latestLog("radarr"); got != "" {
		t.Fatalf("latestLog after removeJailLogs = %q, want empty", got)
	}
}

// A service built from the Makejail is pulled before an update like one naming
// its own image. Adopt writes exactly this Makejail and no `args:--pull`, so
// without reading it an update of an adopted stack rebuilt from the copy the
// host already had and changed nothing.
func TestDirectorServicesReadsTheMakejailImage(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Makejail"), []byte("# Makejail\n\nARG tag=1.2\n\nOPTION container=boot\nOPTION overwrite=force\nOPTION from=ghcr.io/example/app:${tag}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "Other.makejail"), []byte("OPTION from=\"ghcr.io/example/other:${tag}\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "Local.makejail"), []byte("ARG tag=dev\nOPTION from=localhost/example/local:${tag}\n"), 0o644)
	abs := filepath.Join(t.TempDir(), "Abs.makejail")
	os.WriteFile(abs, []byte("ARG tag=9\nARG tag=10\nOPTION from=ghcr.io/example/abs:${tag}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_TAG=3.0\n"), 0o600)
	spec := `services:
  plain:
    name: plain_j
    options:
      - container: 'args:--pull'
  pinned:
    name: pinned_j
    arguments:
      - tag: !ENV '${APP_TAG}'
  own:
    name: own_j
    options:
      - from: ghcr.io/example/redis:7
  other:
    name: other_j
    makejail: Other.makejail
  remote:
    name: remote_j
    makejail: gh+example/remote-makejail
  local:
    name: local_j
    makejail: Local.makejail
  abs:
    name: abs_j
    makejail: ` + abs + `
`
	os.WriteFile(filepath.Join(dir, "appjail-director.yml"), []byte(spec), 0o644)

	got := map[string]string{}
	for _, sj := range NewBackend().serviceJails(&stack.Stack{Name: "app", Dir: dir}) {
		got[sj.svc.Name] = sj.from
	}
	want := map[string]string{
		"plain":  "ghcr.io/example/app:1.2",
		"pinned": "ghcr.io/example/app:3.0",
		"own":    "ghcr.io/example/redis:7",
		"other":  "ghcr.io/example/other:latest",
		"remote": "",
		"local":  "",
		"abs":    "ghcr.io/example/abs:9",
	}
	for svc, w := range want {
		if got[svc] != w {
			t.Errorf("%s: from = %q, want %q", svc, got[svc], w)
		}
	}
}
