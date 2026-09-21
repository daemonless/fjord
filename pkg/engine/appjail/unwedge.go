package appjail

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// A jail's volumes are nullfs mounts into its root, and appjail unmounts them
// as the first step of destroying it. When one of those refuses with "Device
// busy" the destroy stops there and leaves the jail behind -- exit 77, a stack
// that will not delete, and a mount whose own mountpoint no longer exists so
// nothing shows up in fstat to explain it. A plain `umount` still fails; only
// `umount -f` clears it.
//
// So a destroy that fails this way is retried once with the leftovers forced
// off, rather than handed to the operator as "exit status 77".

// busyMountRe is how appjail words it. Matching the message rather than the
// exit status keeps this from swallowing an unrelated failure.
const busyMountMarker = "Device busy"

// containsBusyMount reports whether a destroy stopped on a mount it could not
// take off, as opposed to failing for any other reason.
func containsBusyMount(out string) bool { return strings.Contains(out, busyMountMarker) }

// forceUnmountJail unmounts everything still mounted under a jail's directory,
// forcibly, deepest first. It reports what it cleared.
func forceUnmountJail(ctx context.Context, jail string) ([]string, error) {
	if jail == "" || strings.ContainsAny(jail, " \t\n") {
		return nil, fmt.Errorf("refusing to unmount %q", jail)
	}
	prefix := "/usr/local/appjail/jails/" + jail + "/"
	out, err := exec.CommandContext(ctx, "mount", "-p").Output()
	if err != nil {
		return nil, fmt.Errorf("read the mount table: %w", err)
	}
	var points []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || !strings.HasPrefix(f[1], prefix) {
			continue
		}
		points = append(points, f[1])
	}
	// Deepest first: a mount stacked inside another has to go before its
	// parent, or the parent stays busy for a reason that is not this bug.
	for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
		points[i], points[j] = points[j], points[i]
	}
	var cleared []string
	for _, mp := range points {
		if err := exec.CommandContext(ctx, "umount", "-f", mp).Run(); err != nil {
			return cleared, fmt.Errorf("could not unmount %s: %w", mp, err)
		}
		cleared = append(cleared, mp)
	}
	return cleared, nil
}

// downDestroy runs `down --destroy`, and retries once with the project's
// leftover mounts forced off when that is what stopped it.
func downDestroy(ctx context.Context, s stackDirs, w io.Writer) error {
	var buf strings.Builder
	err := runDirector(ctx, io.MultiWriter(w, &buf), s.Dir(), true, "down", "--destroy")
	if err == nil || !containsBusyMount(buf.String()) {
		return err
	}
	var cleared []string
	for _, jail := range jailsInOutput(buf.String()) {
		got, ferr := forceUnmountJail(ctx, jail)
		cleared = append(cleared, got...)
		if ferr != nil {
			fmt.Fprintf(w, "[fjord] %v\n", ferr)
		}
	}
	if len(cleared) == 0 {
		return err
	}
	fmt.Fprintf(w, "[fjord] a volume was still mounted and would not come off; forced it and retrying: %s\n",
		strings.Join(cleared, ", "))
	return runDirector(ctx, w, s.Dir(), true, "down", "--destroy")
}

// jailsInOutput pulls the jail names appjail named in its own warnings, which
// is where the ones that failed to unmount are reported.
func jailsInOutput(out string) []string {
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		// "... /usr/local/appjail/jails/<name>/jail/..."
		i := strings.Index(line, "/usr/local/appjail/jails/")
		if i < 0 {
			continue
		}
		rest := line[i+len("/usr/local/appjail/jails/"):]
		name, _, ok := strings.Cut(rest, "/")
		if !ok || name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// stackDirs is the little a destroy needs of a stack, so this file can be
// tested without one.
type stackDirs interface{ Dir() string }

// stackDir adapts a plain path to it.
type stackDir string

func (d stackDir) Dir() string { return string(d) }
