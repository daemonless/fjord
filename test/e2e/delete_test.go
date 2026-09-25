//go:build e2e

package e2e

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dataLocation is the host's first app-data location, from fjordd itself.
func dataLocation(t *testing.T) string {
	t.Helper()
	_, body := api(t, "GET", "/api/settings/storage", nil)
	var st struct{ Locations []string }
	decode(t, body, &st)
	if len(st.Locations) == 0 {
		t.Skip("no app-data location configured here")
	}
	return st.Locations[0]
}

// saveNamed saves a stack under an exact name (a compose that has to name its
// own folder cannot use stack()'s generated one) and deletes it at the end.
func saveNamed(t *testing.T, name, compose string) {
	t.Helper()
	if code, body := api(t, "POST", "/api/stacks/"+name+"/save", map[string]any{"compose": compose, "env": "", "engine": "podman"}); code != 200 {
		t.Fatalf("save %s: %d %s", name, code, body)
	}
	t.Cleanup(func() { api(t, "DELETE", "/api/stacks/"+name, nil) })
}

// Delete says what goes and what stays, keeps the app's data unless asked,
// and with data removes exactly its own folder -- never one it only uses
// (#21: deleted stacks left their folders behind with nothing saying where).
func TestDeleteKeepsOrRemovesData(t *testing.T) {
	base := dataLocation(t)
	n := rand.Intn(10000)
	name := fmt.Sprintf("e2e-del-%04d", n)
	own := filepath.Join(base, name)
	shared := filepath.Join(base, fmt.Sprintf("e2e-shared-%04d", n))
	t.Cleanup(func() { shTry(sudo, "rm", "-rf", own, shared) })
	sh(t, sudo, "mkdir", "-p", own+"/config", shared)
	sh(t, sudo, "sh", "-c", "echo data > "+own+"/config/db")
	compose := fmt.Sprintf("services:\n  app:\n    image: %s\n    entrypoint: [\"/bin/sleep\", \"3600\"]\n    network_mode: none\n    volumes:\n      - %s/config:/config\n      - %s:/shared\n",
		fixtureRef, own, shared)

	saveNamed(t, name, compose)
	_, body := api(t, "GET", "/api/stacks/"+name+"/delete-preview", nil)
	var plan struct {
		AppData []struct{ Path string }
		Keeps   []struct{ Path, Why string }
	}
	decode(t, body, &plan)
	if len(plan.AppData) != 1 || plan.AppData[0].Path != own {
		t.Errorf("its own data: %+v, want %s", plan.AppData, own)
	}
	if !strings.Contains(body, shared) {
		t.Errorf("the shared folder is not listed as kept: %s", body)
	}

	if code, b := api(t, "DELETE", "/api/stacks/"+name, nil); code != 200 {
		t.Fatalf("delete: %d %s", code, b)
	}
	if !hostOK(sudo, "test", "-f", own+"/config/db") && !hostOK("test", "-f", own+"/config/db") {
		t.Fatalf("a plain delete removed the app's data")
	}

	saveNamed(t, name, compose)
	if code, b := api(t, "DELETE", "/api/stacks/"+name+"?data=1", nil); code != 200 {
		t.Fatalf("delete with data: %d %s", code, b)
	}
	if _, err := os.Stat(own); err == nil {
		t.Errorf("%s is still there after delete with data", own)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Errorf("the shared folder %s was removed", shared)
	}
}

// Left-over app data lists a folder nothing uses and removes it; a folder a
// container fjord does not manage mounts is neither listed nor removable
// (jupiter's /containers also holds the data of containers Ansible runs).
func TestLeftoverData(t *testing.T) {
	base := dataLocation(t)
	n := rand.Intn(10000)
	free := filepath.Join(base, fmt.Sprintf("e2e-leftover-%04d", n))
	used := filepath.Join(base, fmt.Sprintf("e2e-handrun-%04d", n))
	handrun := fmt.Sprintf("e2e-handrun-%04d", n)
	t.Cleanup(func() {
		shTry("podman", "rm", "-f", handrun)
		shTry(sudo, "rm", "-rf", free, used)
	})
	sh(t, sudo, "mkdir", "-p", free, used)
	sh(t, sudo, "sh", "-c", "echo x > "+free+"/file")
	sh(t, "podman", "run", "-d", "--name", handrun, "--network", "none", "-v", used+":/data", "--entrypoint", "/bin/sleep", fixtureRef, "600")

	_, body := api(t, "GET", "/api/maintenance/leftovers", nil)
	if !strings.Contains(body, `"`+free+`"`) {
		t.Errorf("%s (nothing uses it) is not listed: %s", free, body)
	}
	if strings.Contains(body, `"`+used+`"`) {
		t.Errorf("%s (a hand-run container mounts it) is listed", used)
	}

	remove := func(path string) int {
		code, _ := api(t, "POST", "/api/maintenance/leftovers", map[string]string{"path": path})
		return code
	}
	if code := remove(used); code != 409 {
		t.Errorf("removing a folder a container mounts: %d, want 409", code)
	}
	if code := remove(free); code != 204 {
		t.Fatalf("removing the left-over folder: %d", code)
	}
	if _, err := os.Stat(free); err == nil {
		t.Errorf("%s is still there", free)
	}
	if _, err := os.Stat(used); err != nil {
		t.Errorf("%s was removed", used)
	}
}
