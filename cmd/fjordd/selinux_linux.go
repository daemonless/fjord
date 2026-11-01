package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func selinuxEnforcing() bool {
	b, err := os.ReadFile("/sys/fs/selinux/enforce")
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// selinuxType is a path's SELinux type ("container_file_t"), "" if unknown.
func selinuxType(path string) string {
	buf := make([]byte, 256)
	n, err := syscall.Getxattr(path, "security.selinux", buf)
	if err != nil || n == 0 {
		return ""
	}
	f := strings.Split(strings.TrimRight(string(buf[:n]), "\x00"), ":")
	if len(f) < 3 {
		return ""
	}
	return f[2]
}

func labelForContainers(path string) error {
	out, err := exec.Command("chcon", "-R", "-t", "container_file_t", path).CombinedOutput()
	if err != nil {
		return &labelError{strings.TrimSpace(string(out)), err}
	}
	return nil
}

type labelError struct {
	out string
	err error
}

func (e *labelError) Error() string { return e.out + ": " + e.err.Error() }
