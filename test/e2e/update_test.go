//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// A container on an old build is reported behind even when the local tag is
// current -- judged by what the container runs, not by the tag (#14: pulling
// a shared tag used to make every other stack on it read "current").
func TestStaleContainerDetected(t *testing.T) {
	advance := staleFixture(t)
	name := stack(t, "services:\n"+sleeper("a"))
	action(t, name, "up", nil)
	advance() // the local tag is now current; the container is not

	id := containers(t, name)["a"]
	if got := runningDigest(t, id); got != oldDigest {
		t.Fatalf("fixture: container runs %s, want the old build %s", got, oldDigest)
	}
	state, svcs := check(t, name)
	if state != "available" || svcs["a"].State != "available" {
		t.Errorf("update-check = %s / %+v, want available", state, svcs["a"])
	}
}

// Updating one service recreates only it: the other keeps its container
// (#14 -- and a partial update must never carry --remove-orphans, which
// deleted the services not named).
func TestPartialUpdateLeavesOthers(t *testing.T) {
	advance := staleFixture(t)
	name := stack(t, "services:\n"+sleeper("a")+sleeper("b"))
	action(t, name, "up", nil)
	advance()
	before := containers(t, name)

	out := action(t, name, "update", map[string]any{"services": []string{"b"}})
	if failed(out) {
		t.Fatalf("update failed:\n%s", out)
	}
	after := containers(t, name)
	if after["a"] == "" || after["a"] != before["a"] {
		t.Errorf("a was touched: %s -> %s", before["a"], after["a"])
	}
	if after["b"] == "" || after["b"] == before["b"] {
		t.Errorf("b was not recreated: %s -> %s", before["b"], after["b"])
	}
	if strings.Contains(out, "--remove-orphans") {
		t.Errorf("a partial update carried --remove-orphans:\n%s", out)
	}
	_, svcs := check(t, name)
	if svcs["a"].State != "available" || svcs["b"].State != "current" {
		t.Errorf("after: a=%s b=%s, want a available, b current", svcs["a"].State, svcs["b"].State)
	}
}

// An update whose new container dies is a failed update, even though the
// recreate itself went fine (#14 health watch: restart: always reads
// "running" between crashes).
func TestCrashLoopFailsUpdate(t *testing.T) {
	name := stack(t, "services:\n  app:\n    image: "+fixtureRef+
		"\n    entrypoint: [\"/bin/sh\", \"-c\", \"sleep 5; exit 1\"]\n    restart: always\n    network_mode: none\n")
	action(t, name, "up", nil)
	out := action(t, name, "update", nil)
	if !failed(out) || !strings.Contains(out, "crash-looping") {
		t.Errorf("a crash-looping update was not failed:\n%s", out)
	}
}

// A healthy update passes the watch and says so.
func TestHealthyUpdatePasses(t *testing.T) {
	name := stack(t, "services:\n"+sleeper("app"))
	action(t, name, "up", nil)
	out := action(t, name, "update", nil)
	if failed(out) || !strings.Contains(out, "app has stayed up") {
		t.Errorf("a healthy update:\n%s", out)
	}
}

// Rollback returns to the image the last update replaced -- from the
// registry, because the old image is gone after a prune -- and pins it;
// unpin restores the tag.
func TestRollbackFromRegistry(t *testing.T) {
	advance := staleFixture(t)
	// By ID: once the tag moves, the old image loses its RepoDigests and can
	// no longer be named by digest.
	oldID := sh(t, "podman", "image", "inspect", fixtureRef, "--format", "{{.Id}}")
	name := stack(t, "services:\n"+sleeper("app"))
	action(t, name, "up", nil)
	advance()
	if out := action(t, name, "update", map[string]any{"services": []string{"app"}}); failed(out) {
		t.Fatalf("update failed:\n%s", out)
	}
	if d := runningDigest(t, containers(t, name)["app"]); d == oldDigest {
		t.Fatalf("update did not move off the old build")
	}
	// What a prune does: the old build is no longer on the host.
	shTry("podman", "rmi", "-f", oldID)
	if podmanOK("image", "exists", oldID) {
		t.Fatalf("fixture: could not remove the old build %s", oldID)
	}

	out := action(t, name, "rollback", map[string]any{"services": []string{"app"}})
	if failed(out) {
		t.Fatalf("rollback failed:\n%s", out)
	}
	if d := runningDigest(t, containers(t, name)["app"]); d != oldDigest {
		t.Errorf("after rollback the container runs %s, want %s", d, oldDigest)
	}
	_, body := api(t, "GET", "/api/stacks/"+name, nil)
	if !strings.Contains(body, "@"+oldDigest) {
		t.Errorf("rollback did not pin the compose to %s", oldDigest)
	}

	if code, b := api(t, "POST", "/api/stacks/"+name+"/unpin", map[string]string{"service": "app"}); code != 204 {
		t.Fatalf("unpin: %d %s", code, b)
	}
	_, body = api(t, "GET", "/api/stacks/"+name, nil)
	if strings.Contains(body, "@sha256") {
		t.Errorf("unpin left a digest in the compose")
	}
}

// A save made to a compose fjord has rewritten since is refused (#14 -- two
// Saves from a stale editor undid a rollback).
func TestSaveRefusedWhenStale(t *testing.T) {
	name := stack(t, "services:\n"+sleeper("app"))
	var detail struct{ ComposeHash string }
	_, body := api(t, "GET", "/api/stacks/"+name, nil)
	decode(t, body, &detail)

	changed := "services:\n" + sleeper("app") + "# edited\n"
	if code, b := api(t, "POST", "/api/stacks/"+name+"/save", map[string]any{"compose": changed, "env": "", "baseHash": detail.ComposeHash}); code != 200 {
		t.Fatalf("save with the current hash: %d %s", code, b)
	}
	if code, _ := api(t, "POST", "/api/stacks/"+name+"/save", map[string]any{"compose": changed, "env": "", "baseHash": detail.ComposeHash}); code != 409 {
		t.Errorf("save with a stale hash: %d, want 409", code)
	}
}
