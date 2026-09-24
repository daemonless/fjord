//go:build e2e

package e2e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// An update succeeds with a terminal open in the container (#14: podman
// refuses to remove a container with an active exec session, podman-compose
// exits 0 anyway, and the update used to report success on the old one).
func TestUpdateWithOpenShell(t *testing.T) {
	name := stack(t, "services:\n"+sleeper("app"))
	action(t, name, "up", nil)
	before := containers(t, name)["app"]

	ws := strings.Replace(base, "http", "ws", 1) + "/api/stacks/" + name + "/exec?container=" + name + "_app_1"
	conn, _, err := websocket.DefaultDialer.Dial(ws, nil)
	if err != nil {
		t.Fatalf("open a terminal: %v", err)
	}
	defer conn.Close()
	time.Sleep(2 * time.Second) // the shell is up inside the container

	out := action(t, name, "update", nil)
	if failed(out) {
		t.Fatalf("update with a shell open failed:\n%s", out)
	}
	if after := containers(t, name)["app"]; after == before {
		t.Errorf("container was not replaced")
	}
}

// Policies decide per the plan, and the candidate's first sighting is kept.
func TestUpdatePolicyVerdicts(t *testing.T) {
	name := stack(t, "services:\n  zensical:\n    image: ghcr.io/daemonless/zensical:0.0.63\n    entrypoint: [\"/bin/sleep\", \"3600\"]\n    network_mode: none\n")
	action(t, name, "up", nil)
	if state, _ := check(t, name); state != "upgrade" {
		t.Fatalf("fixture: 0.0.63 should have a newer version, got %s", state)
	}
	for policy, want := range map[string]string{
		"off":      "auto-update is off",
		"notify":   "notify only",
		"rebuilds": "does not take a patch",
		"patch":    "soaking",
	} {
		if code, b := api(t, "POST", "/api/stacks/"+name+"/policy", map[string]string{"policy": policy}); code != 204 {
			t.Fatalf("set %s: %d %s", policy, code, b)
		}
		_, body := api(t, "GET", "/api/stacks/"+name+"/changes?service=zensical", nil)
		var c struct {
			Class string
			Auto  struct {
				Act    bool
				Reason string
			}
		}
		decode(t, body, &c)
		if c.Class != "patch" || c.Auto.Act || !strings.Contains(c.Auto.Reason, want) {
			t.Errorf("policy %s: class %s, %+v, want %q", policy, c.Class, c.Auto, want)
		}
	}
	if code, _ := api(t, "POST", "/api/stacks/"+name+"/policy", map[string]string{"policy": "sometimes"}); code != 400 {
		t.Errorf("an unknown policy was accepted: %d", code)
	}
	seen := sh(t, sudo, "cat", envOr("E2E_FJORD_ROOT", "/var/db/fjord")+"/update-first-seen.json")
	var m map[string]string
	if json.Unmarshal([]byte(seen), &m) != nil || m["ghcr.io/daemonless/zensical:0.0.64"] == "" {
		t.Errorf("zensical:0.0.64 not in first-seen: %s", seen)
	}
}

// needAppJail skips on hosts without the appjail engine.
func needAppJail(t *testing.T) {
	t.Helper()
	if _, body := api(t, "GET", "/api/setup", nil); !strings.Contains(body, `"appjail"`) || !hostOK("appjail-director", "--version") {
		t.Skip("no appjail engine here")
	}
}

// directorStack saves a director stack and deletes it at the end.
func directorStack(t *testing.T, director, makejail string) (string, int, string) {
	t.Helper()
	name := "e2e-aj-" + time.Now().Format("150405")
	code, body := api(t, "POST", "/api/stacks/"+name+"/save", map[string]any{
		"compose": "", "env": "DIRECTOR_PROJECT=" + name + "\n", "director": director, "makejail": makejail, "engine": "appjail",
	})
	t.Cleanup(func() { api(t, "DELETE", "/api/stacks/"+name, nil) })
	return name, code, body
}

// A director stack whose every service names its own makejail needs no local
// one; a service without one does (#16: people saved a "#" to get past it).
func TestAppJailMakejailOptional(t *testing.T) {
	needAppJail(t)
	if _, code, body := directorStack(t, "services:\n  ds:\n    makejail: gh+AppJail-makejails/documentserver\n", ""); code != 200 {
		t.Errorf("own makejail, no local: %d %s", code, body)
	}
	time.Sleep(time.Second) // a different stack name
	if _, code, body := directorStack(t, "services:\n  app:\n    name: app\n", ""); code != 400 || !strings.Contains(body, "names no makejail") {
		t.Errorf("service without a makejail: %d %s", code, body)
	}
}

// A failed director run says why in Output and is marked failed (#16: the
// reason was only in /root/.director/logs, and the start read as success).
func TestAppJailFailureShowsLog(t *testing.T) {
	needAppJail(t)
	name, code, body := directorStack(t, "services:\n  app:\n    name: e2ebadapp\n    makejail: gh+AppJail-makejails/fjord-e2e-no-such-makejail\n", "")
	if code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	out := action(t, name, "up", nil)
	if !failed(out) || !strings.Contains(out, "makejail.log") || !strings.Contains(out, "Repository not found") {
		t.Errorf("a failed director run:\n%s", out)
	}
}
