package main

import (
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
)

// A stack's own private segment is not where new installs go: the Networks
// page offered immich_priv as a default.
func TestNotADefault(t *testing.T) {
	if msg := notADefault(engine.Network{Name: "lan"}); msg != "" {
		t.Errorf("lan refused: %q", msg)
	}
	msg := notADefault(engine.Network{Name: "immich_priv", Private: true, OwnedBy: "immich"})
	if !strings.Contains(msg, "immich_priv is immich's private network") {
		t.Errorf("private accepted or unclear: %q", msg)
	}
}
