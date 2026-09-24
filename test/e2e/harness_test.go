//go:build e2e

// Package e2e drives a live fjordd the way the UI does and checks outcomes --
// container IDs, the digest a container runs, what the Output says -- never
// exit codes. Every case builds its own throwaway stacks (named e2e-...) and
// removes them, pass or fail.
//
//	go test -tags e2e ./test/e2e -v -count=1
//
// FJORD_URL   fjordd to drive (default http://127.0.0.1:3567)
// E2E_SUDO    how to run podman as root (default doas; "" when already root)
//
// It runs podman on the host fjordd manages, and moves the local
// mariadb:10.11 tag while it runs (restored after): run it on a dev host.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

var (
	base = envOr("FJORD_URL", "http://127.0.0.1:3567")
	sudo = envOr("E2E_SUDO", "doas")
)

func envOr(k, d string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return d
}

// The fixture image: an old build of mariadb:10.11 still served by ghcr.io
// by digest. Pointing the local tag at it makes a container that is
// provably stale, every run. Its entrypoint is overridden to sleep, so no
// s6 and nothing that can crash-loop.
const (
	fixtureRef = "ghcr.io/daemonless/mariadb:10.11"
	oldDigest  = "sha256:5b017e376eebce1fa6324c6add6893905c26ea937b791e7d3cb323b04db2af6f"
)

// sh runs a host command (through sudo when it's podman) and returns stdout.
func sh(t *testing.T, name string, args ...string) string {
	t.Helper()
	if sudo != "" && name == "podman" {
		args = append([]string{name}, args...)
		name = sudo
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// api calls fjordd and returns the status and body.
func api(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	cl := &http.Client{Timeout: 10 * time.Minute}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// stack creates a throwaway stack from compose and deletes it at the end.
func stack(t *testing.T, compose string) string {
	t.Helper()
	name := fmt.Sprintf("e2e-%s-%04d", strings.ToLower(t.Name()[4:min(len(t.Name()), 14)]), rand.Intn(10000))
	name = strings.NewReplacer("_", "-", "/", "-").Replace(name)
	if code, body := api(t, "POST", "/api/stacks/"+name+"/save", map[string]any{"compose": compose, "env": "", "engine": "podman"}); code != 200 {
		t.Fatalf("save %s: %d %s", name, code, body)
	}
	t.Cleanup(func() {
		api(t, "DELETE", "/api/stacks/"+name, nil)
		os.RemoveAll("/containers/" + name) // best effort; data dirs are kept by delete
	})
	return name
}

// action runs a streamed stack action and returns its Output.
func action(t *testing.T, name, what string, body any) string {
	t.Helper()
	code, out := api(t, "POST", "/api/stacks/"+name+"/"+what, body)
	if code != 200 {
		t.Fatalf("%s %s: %d %s", what, name, code, out)
	}
	return out
}

// failed reports whether an Output marks the action failed, as the UI reads it.
func failed(out string) bool {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "[error]") {
			return true
		}
	}
	return false
}

// containers maps each compose service of a stack to its container ID.
func containers(t *testing.T, name string) map[string]string {
	t.Helper()
	out := sh(t, "podman", "ps", "-a", "--no-trunc",
		"--filter", "label=io.podman.compose.project="+name,
		"--format", `{{index .Labels "io.podman.compose.service"}} {{.ID}}`)
	m := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			m[f[0]] = f[1]
		}
	}
	return m
}

// runningDigest is the registry digest a container was created from.
func runningDigest(t *testing.T, id string) string {
	t.Helper()
	return sh(t, "podman", "inspect", id, "--format", "{{.ImageDigest}}")
}

// check is the stack's update-check, per service.
type svcCheck struct {
	Service, State, Running, Latest string
}

func check(t *testing.T, name string) (string, map[string]svcCheck) {
	t.Helper()
	_, body := api(t, "GET", "/api/stacks/"+name+"/update-check", nil)
	var st struct {
		State    string
		Services []svcCheck
	}
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatalf("update-check %s: %v: %s", name, err, body)
	}
	m := map[string]svcCheck{}
	for _, s := range st.Services {
		m[s.Service] = s
	}
	return st.State, m
}

// staleFixture points the local fixture tag at the old build and returns a
// function that moves it to the current one -- which makes every container
// created in between stale. The tag is restored when the test ends.
func staleFixture(t *testing.T) (advance func()) {
	t.Helper()
	sh(t, "podman", "pull", "-q", fixtureRef+"@"+oldDigest)
	sh(t, "podman", "tag", fixtureRef+"@"+oldDigest, fixtureRef)
	advance = func() { sh(t, "podman", "pull", "-q", fixtureRef) }
	t.Cleanup(advance)
	return advance
}

// sleeper is a compose service that does nothing, on no network.
func sleeper(name string) string {
	return fmt.Sprintf("  %s:\n    image: %s\n    entrypoint: [\"/bin/sleep\", \"3600\"]\n    network_mode: none\n", name, fixtureRef)
}

// shTry is sh for commands allowed to fail (cleanup, "already gone").
func shTry(name string, args ...string) {
	if sudo != "" && name == "podman" {
		args = append([]string{name}, args...)
		name = sudo
	}
	exec.Command(name, args...).Run()
}

func decode(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
}

// podmanOK reports whether a podman command succeeds.
func podmanOK(args ...string) bool {
	name := "podman"
	if sudo != "" {
		args = append([]string{name}, args...)
		name = sudo
	}
	return exec.Command(name, args...).Run() == nil
}
