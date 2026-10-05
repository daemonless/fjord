package podman

import (
	"errors"
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
