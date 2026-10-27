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

var pullFailed = regexp.MustCompile(`unable to copy from source docker://(\S+?): (.+)$`)

// pullError says, for a stack compose made nothing of, that its image could
// not be downloaded and why; "" when the output shows no failed pull. The
// failure scrolled past and the stack was left with "compose created no
// containers to start": forgejo:latest, a tag never published, read as a
// broken compose file.
func pullError(out string) string {
	var image, why string
	for _, line := range strings.Split(out, "\n") {
		if m := pullFailed.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			image, why = m[1], m[2]
		}
	}
	if image == "" {
		return ""
	}
	switch {
	case strings.Contains(why, "manifest unknown"):
		return "the image " + image + " could not be downloaded: the registry has no such version. Pick another version for it"
	case strings.Contains(why, "unauthorized") || strings.Contains(why, "denied"):
		return "the image " + image + " could not be downloaded: the registry refused it (a private image, or a misspelt name)"
	case strings.Contains(why, "no such host") || strings.Contains(why, "dial tcp") || strings.Contains(why, "timeout"):
		return "the image " + image + " could not be downloaded: the registry could not be reached. Check this host's internet connection"
	}
	return "the image " + image + " could not be downloaded: " + why
}
