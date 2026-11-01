package main

import (
	"path/filepath"
	"slices"
	"strings"
)

// systemDirs are never an App data location: everything inside one that no
// stack uses is listed as left-over app data, whose Delete removes as root
// (review 1.2: /var listed /var/db and /var/log).
var systemDirs = []string{
	"/", "/bin", "/boot", "/compat", "/dev", "/etc", "/home", "/lib", "/lib32", "/lib64", "/libexec",
	"/media", "/mnt", "/opt", "/proc", "/rescue", "/root", "/run", "/sbin", "/srv", "/sys", "/tmp",
	"/usr", "/usr/local", "/var", "/var/cache", "/var/db", "/var/lib", "/var/log", "/var/run", "/var/tmp", "/zroot",
}

// unsafeDataLocation says why p cannot be an App data location, or "".
func (s *server) unsafeDataLocation(p string) string {
	p = filepath.Clean(p)
	if slices.Contains(systemDirs, p) {
		return p + " is a system folder"
	}
	if root := filepath.Clean(s.fjordRoot); p == root || strings.HasPrefix(root, p+"/") {
		return p + " holds fjord's own folder (" + root + ")"
	}
	return ""
}

// resolved is p with its symlinks followed, as far as they exist: a mount
// named through a link is the folder it points at.
func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
