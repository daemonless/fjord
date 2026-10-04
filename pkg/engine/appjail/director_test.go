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
