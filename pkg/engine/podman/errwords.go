package podman

import (
	"regexp"
	"strings"
)

// A failed command left "start: exit status 125" as the stack's failure,
// while podman's own reason -- "Error: ... source path does not exist",
// "address already in use" -- had scrolled past in the Output pane. The
// recorded failure is the first [error] line, so that line has to carry the
// reason itself.

// containerID is the noise around podman's reason: `unable to start
// container "21f9f8...": `.
var containerID = regexp.MustCompile(`unable to (start|create|stop|remove) container "?[0-9a-f]{12,64}"?: `)

// commandError is why a command failed, in podman's words when it printed
// any: its last "Error: ..." line from out, without the container id. err
// (an exit status) only when podman said nothing.
func commandError(out string, err error) string {
	var last string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Error: ") {
			last = strings.TrimSpace(line)
		}
	}
	if last == "" {
		if err == nil {
			return ""
		}
		return err.Error()
	}
	last = strings.TrimPrefix(last, "Error: ")
	last = containerID.ReplaceAllString(last, "")
	last = strings.TrimPrefix(last, "OCI runtime error: ")
	return last
}
