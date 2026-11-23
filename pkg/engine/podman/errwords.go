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
	if strings.Contains(last, "aardvark-dns failed to start") {
		return aardvarkFailed
	}
	return last
}

// aardvarkFailed is netavark's "error while applying dns entries:
// aardvark-dns failed to start: Error from child process" in words that say
// where to look: the commonest cause is a DNS server on port 53 of every
// address, which the System page checks for.
const aardvarkFailed = "podman's DNS server (aardvark-dns) could not start, so containers on a network with name resolution cannot either. " +
	"When another DNS server holds port 53 on this host, the System page's check \"container name resolution can start (port 53)\" moves podman's off it"

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
	return pullWhy(image, why)
}

// pullWhy words podman's reason for a failed pull of image.
func pullWhy(image, why string) string {
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

var noSuchNetwork = regexp.MustCompile(`External network \[([^\]]+)\] does not exists?`)

// missingNetwork says, for a stack compose made nothing of, that a network
// it names is not on this host; "" when the output shows no such thing.
// podman-compose's "RuntimeError: External network [x] does not exists"
// scrolled past and the stack was left with "compose created no containers
// to start".
func missingNetwork(out string) string {
	// The last one: the traceback above it quotes podman-compose's own source
	// line, "External network [{net_name}] does not exists".
	all := noSuchNetwork.FindAllStringSubmatch(out, -1)
	if len(all) == 0 {
		return ""
	}
	m := all[len(all)-1]
	return "the network " + m[1] + " that this stack uses does not exist on this host. Make it again on the Networks page, then Start " +
		"(a network made before podman moved to another network backend has to be made again)"
}

var composeRaised = regexp.MustCompile(`(?m)^(?:RuntimeError|ValueError|KeyError): (.+)$`)

// composeRefusal says, for a stack compose made nothing of, what
// podman-compose itself objected to in the compose file; "" when it raised
// nothing. Its reason ("missing networks: default") scrolled past in a
// traceback and the stack was left with "compose created no containers".
func composeRefusal(out string) string {
	all := composeRaised.FindAllStringSubmatch(out, -1)
	if len(all) == 0 {
		return ""
	}
	return "podman-compose does not accept this stack's compose file: " + strings.TrimSpace(all[len(all)-1][1])
}
