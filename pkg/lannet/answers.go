package lannet

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// Answers reports whether something on iface's wire holds ip, and whether
// it could be checked at all. A pool's IPAM knows only what it handed out
// itself: it gave a container .4, which another host on the LAN had.
//
// The probe is arping's duplicate address detection: it asks from 0.0.0.0,
// so it claims nothing -- putting the address on an interface to test it
// would announce it, the very harm being avoided. Linux only (iputils
// arping); FreeBSD's base has no such probe, so there it is not checked.
func Answers(ctx context.Context, iface, ip string) (held, checked bool) {
	if !CanCheck() || iface == "" {
		return false, false
	}
	bin := iputilsArping()
	// -D exits 1 when a reply came, 0 when none did within the wait.
	err := exec.CommandContext(ctx, bin, "-D", "-q", "-c", "2", "-w", "2", "-I", iface, ip).Run()
	if err == nil {
		return false, true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return true, true
	}
	return false, false
}

// CanCheck reports whether Answers can check anything on this host.
func CanCheck() bool { return runtime.GOOS == "linux" && iputilsArping() != "" }

// iputilsArping is the path of iputils' arping, or "". Other arpings (Thomas
// Habets', busybox's) take other flags and exit codes: what they would say
// cannot be trusted, so they count as none.
var iputilsArping = sync.OnceValue(func() string {
	bin, err := exec.LookPath("arping")
	if err != nil {
		return ""
	}
	out, _ := exec.Command(bin, "-V").CombinedOutput()
	if !strings.Contains(string(out), "iputils") {
		return ""
	}
	return bin
})
