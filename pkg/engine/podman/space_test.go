package podman

import (
	"testing"

	"github.com/daemonless/fjord/pkg/stack"
)

func TestSpaceVerdict(t *testing.T) {
	const gb = 1_000_000_000
	for _, c := range []struct {
		name          string
		need, free    uint64
		refuse, warns bool
	}{
		{"fits with room", 1 * gb, 10 * gb, false, false},
		{"fits, little room to unpack", 1 * gb, 2 * gb, false, true},
		{"the download alone does not fit", 1400 * 1_000_000, 900 * 1_000_000, true, false},
		{"size unknown never blocks", 0, 0, false, false},
	} {
		refuse, warn := spaceVerdict(c.need, c.free)
		if refuse != c.refuse || warn != c.warns {
			t.Errorf("%s: refuse=%v warn=%v, want %v %v", c.name, refuse, warn, c.refuse, c.warns)
		}
	}
}

func TestServicesImages(t *testing.T) {
	s := &stack.Stack{Compose: "services:\n  app:\n    image: a:1\n  db:\n    image: b:2\n  build-only:\n    build: .\n"}
	if got := servicesImages(s, nil); len(got) != 2 {
		t.Errorf("all: %v", got)
	}
	if got := servicesImages(s, []string{"db"}); len(got) != 1 || got[0] != "b:2" {
		t.Errorf("db: %v", got)
	}
}
