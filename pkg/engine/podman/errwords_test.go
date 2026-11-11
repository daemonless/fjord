package podman

import (
	"errors"
	"strings"
	"testing"
)

func TestCommandError(t *testing.T) {
	exit := errors.New("exit status 125")
	// saturn 2026-10-04: a start whose bind source was missing.
	out := "$ podman start t-startfail\n" +
		`Error: OCI runtime error: unable to start container "21f9f8f67d61c6964fa5459b4fa2be30404f83c879f0b29250bf482056a0b410": ` +
		`ocijail: mounting {"destination":"/x"}: source path does not exist: /nonexistent-fjord-test/x (create the directory first)` + "\n"
	want := `ocijail: mounting {"destination":"/x"}: source path does not exist: /nonexistent-fjord-test/x (create the directory first)`
	if got := commandError(out, exit); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// The last Error line is the one that stopped it.
	if got := commandError("Error: first\nsome output\nError: second\n", exit); got != "second" {
		t.Errorf("got %q, want the last Error line", got)
	}
	// Podman said nothing: the exit status is all there is.
	if got := commandError("pulling...\n", exit); got != "exit status 125" {
		t.Errorf("got %q", got)
	}
}

func TestPullError(t *testing.T) {
	forgejo := "$ podman pull ghcr.io/daemonless/forgejo:latest\nTrying to pull ghcr.io/daemonless/forgejo:latest...\n" +
		"Error: unable to copy from source docker://ghcr.io/daemonless/forgejo:latest: initializing source docker://ghcr.io/daemonless/forgejo:latest: reading manifest latest in ghcr.io/daemonless/forgejo: manifest unknown\n" +
		"Error: no container with name or ID \"sw-forgejo_forgejo_1\" found: no such container\n"
	if got := pullError(forgejo); !strings.Contains(got, "ghcr.io/daemonless/forgejo:latest") || !strings.Contains(got, "no such version") {
		t.Errorf("manifest unknown: %q", got)
	}
	// podman-compose 1.6 on Linux exits 125 and says so after the reason.
	linux := "Trying to pull docker.io/library/busybox:nope...\n" +
		"Error: unable to copy from source docker://busybox:nope: initializing source docker://busybox:nope: reading manifest nope in docker.io/library/busybox: manifest unknown\n" +
		"Error: executing /usr/bin/podman-compose pull: exit status 125\n"
	if got := pullError(linux); !strings.Contains(got, "busybox:nope") || !strings.Contains(got, "no such version") {
		t.Errorf("podman-compose 1.6: %q", got)
	}
	if got := pullError("Error: no container with name or ID \"x\" found\n"); got != "" {
		t.Errorf("no pull in it: %q", got)
	}
}

// A failed pull is said in podman's words when it has them, and still said
// when it does not: the caller stops before compose tears anything down.
func TestPullFailure(t *testing.T) {
	out := "Trying to pull ghcr.io/x/app:nope...\nError: unable to copy from source docker://ghcr.io/x/app:nope: initializing source docker://ghcr.io/x/app:nope: reading manifest nope in ghcr.io/x/app: manifest unknown\n"
	if got := pullFailure(out, "ghcr.io/x/app:nope", errors.New("exit status 125")); !strings.Contains(got, "no such version") {
		t.Errorf("manifest unknown: %q", got)
	}
	if got := pullFailure("", "ghcr.io/x/app:nope", errors.New("exit status 125")); !strings.Contains(got, "ghcr.io/x/app:nope could not be downloaded") {
		t.Errorf("no output: %q", got)
	}
}
