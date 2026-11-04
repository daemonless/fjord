package engine

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// NewShellEnv returns a unique FJORD_SHELL=<hex> environment assignment tagged
// onto an interactive shell session so Close can reap background children the
// shell spawned inside the jail.
func NewShellEnv() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "FJORD_SHELL=" + hex.EncodeToString(b[:])
}

// ReapShellByEnv kills any process on a FreeBSD host (optionally scoped to
// jail) whose environment or command carries shellEnv. Background jobs started
// from a web shell survive the shell's own exit and hold the jail or podman
// exec session open until reaped.
func ReapShellByEnv(shellEnv, jail string) {
	if shellEnv == "" || runtime.GOOS != "freebsd" {
		return
	}
	args := []string{"-ax", "-e", "-o", "pid=", "-o", "command="}
	if jail != "" {
		args = []string{"-ax", "-e", "-J", jail, "-o", "pid=", "-o", "command="}
	}
	out, err := exec.Command("/bin/ps", args...).Output()
	if err != nil {
		return
	}
	self := os.Getpid()
	for _, pid := range PidsWithEnv(string(out), shellEnv) {
		if pid != self {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// PidsWithEnv parses `ps -ax -e -o pid= -o command=` output and returns every
// PID (> 1) whose whitespace-separated tokens include shellEnv.
func PidsWithEnv(psOut, shellEnv string) []int {
	if shellEnv == "" {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(psOut, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 1 {
			continue
		}
		for _, tok := range fields[1:] {
			if tok == shellEnv {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}
