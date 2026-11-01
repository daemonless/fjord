package main

import (
	"testing"
	"time"
)

func TestNoShellOutputIsTheRuntimeRefusingToStart(t *testing.T) {
	crun := []byte("Error: crun: executable file `/bin/sh` not found: No such file or directory: OCI runtime attempted to invoke a command that was not found\r\n")
	if !noShellOutput(crun, int64(len(crun)), 300*time.Millisecond) {
		t.Fatal("crun's refusal should read as no shell")
	}
	// A shell that ran, and in which someone typed a command that is missing.
	typed := []byte("/ # foo\r\nsh: foo: not found\r\n/ # exit\r\n")
	if noShellOutput(typed, int64(len(typed)), 40*time.Second) {
		t.Fatal("a shell that ran for a while is not a missing shell")
	}
	if noShellOutput(typed, int64(len(typed)), time.Second) {
		t.Fatal("'not found' without 'executable' or /bin/sh is the shell talking")
	}
	if noShellOutput(crun, 100000, time.Second) {
		t.Fatal("a session that produced real output ran a shell")
	}
}
