package engine

import (
	"reflect"
	"strings"
	"testing"
)

func TestNewShellEnvUnique(t *testing.T) {
	a, b := NewShellEnv(), NewShellEnv()
	if !strings.HasPrefix(a, "FJORD_SHELL=") || len(a) != len("FJORD_SHELL=")+16 || a == b {
		t.Fatalf("NewShellEnv() = %q, %q", a, b)
	}
}

// A web shell's background children inherit its FJORD_SHELL=<token> in their
// environment; PidsWithEnv matches only that exact token and ignores pid 1 or
// other sessions.
func TestPidsWithEnv(t *testing.T) {
	psOut := `
    1 PATH=/sbin:/bin FJORD_SHELL=a1b2c3d4 /sbin/init
 4120 PATH=/sbin:/bin TERM=xterm-256color FJORD_SHELL=a1b2c3d4 /bin/sh
 4188 PATH=/sbin:/bin TERM=xterm-256color FJORD_SHELL=a1b2c3d4 sleep 300
 4201 PATH=/sbin:/bin TERM=xterm-256color FJORD_SHELL=a1b2c3d400 /bin/sh
 5012 PATH=/sbin:/bin sshd: root@pts/0 (sshd)
`
	got := PidsWithEnv(psOut, "FJORD_SHELL=a1b2c3d4")
	want := []int{4120, 4188}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PidsWithEnv = %v, want %v", got, want)
	}
}
