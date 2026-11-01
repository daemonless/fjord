package main

import (
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
)

// An engine for another OS is not offered at all: on Linux AppJail came up
// in Settings with an Install button for a FreeBSD package, and as a
// checkbox on the first Setup screen.
func TestEnginesForOS(t *testing.T) {
	all := []engine.Descriptor{{Name: "podman"}, {Name: "appjail", OS: "freebsd"}}
	names := func(ds []engine.Descriptor) (out []string) {
		for _, d := range ds {
			out = append(out, d.Name)
		}
		return out
	}
	if got := names(enginesFor(all, "linux")); len(got) != 1 || got[0] != "podman" {
		t.Errorf("linux: %v", got)
	}
	if got := names(enginesFor(all, "freebsd")); len(got) != 2 {
		t.Errorf("freebsd: %v", got)
	}
}
