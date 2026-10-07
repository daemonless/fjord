package doctor

import (
	"strings"
	"testing"
)

// The API service outlives `pkg delete podman` and keeps answering: Setup
// said "podman API socket: ok" on a host with no podman at all.
func TestAnsweringSocketWithPodmanGone(t *testing.T) {
	if st, _ := answeringSocket("/var/run/podman/podman.sock", true); st != OK {
		t.Errorf("installed: %v", st)
	}
	st, detail := answeringSocket("/var/run/podman/podman.sock", false)
	if st != Fail || !strings.Contains(detail, "podman is not installed") {
		t.Errorf("podman gone: %v %q", st, detail)
	}
}
