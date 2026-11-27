package podman

import (
	"strings"
	"testing"
)

// A network left by the other backend is explained the right way round.
func TestLegacyNote(t *testing.T) {
	if got := legacyNote(backendNetavark); got != legacyProblem {
		t.Errorf("on netavark: %q", got)
	}
	if got := legacyNote(backendCNI); !strings.Contains(got, "made for the netavark network backend") || !strings.Contains(got, "now uses CNI") {
		t.Errorf("on CNI: %q", got)
	}
}
