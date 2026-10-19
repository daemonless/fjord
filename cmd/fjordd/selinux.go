package main

import (
	"fmt"
	"slices"
	"strings"
)

// containerLabels are the SELinux types a container may use as a bind mount.
var containerLabels = []string{"container_file_t", "container_ro_file_t", "svirt_sandbox_file_t"}

// selinuxAction is what preflight does about one bind mount on a host where
// SELinux is enforcing: label it for containers (what podman's :z does) when
// it is fjord's own folder, or say what to do when it is someone else's.
// Without either, every app with a data folder got "Permission denied" on
// Fedora. typ is the folder's SELinux type; opts the mount's options.
func selinuxAction(path, typ, opts string, ours bool) (label bool, problem string) {
	for _, o := range strings.Split(opts, ",") {
		if o == "z" || o == "Z" {
			return false, "" // podman relabels it itself
		}
	}
	if typ == "" || slices.Contains(containerLabels, typ) {
		return false, ""
	}
	if ours {
		return true, ""
	}
	return false, fmt.Sprintf("bind mount %s: SELinux will refuse it to the container (its type is %s). "+
		"Add :z to the mount, or label it: chcon -R -t container_file_t %s", path, typ, path)
}
