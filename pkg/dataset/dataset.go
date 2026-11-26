// Package dataset gives an app its own ZFS dataset when its data location
// is a dataset's mountpoint: <parent>/<stack>, every property inherited,
// tagged so fjord can prove it made it before it ever destroys it.
package dataset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Tag is the user property fjord sets on a dataset it made: its stack.
const Tag = "fjord:stack"

// run is the zfs command; tests replace it.
var run = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "zfs", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, errors.New(msg)
		}
	}
	return out, err
}

// At names the dataset mounted exactly at dir, if there is one. No zfs, no
// pool, or a plain folder: ok is false.
func At(ctx context.Context, dir string) (name string, ok bool) {
	dir = filepath.Clean(dir)
	out, err := run(ctx, "list", "-H", "-o", "name,mountpoint", "-t", "filesystem")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 2 && filepath.Clean(f[1]) == dir {
			return f[0], true
		}
	}
	return "", false
}

// Create makes <parent>/<stack>, mounted under the parent's mountpoint by
// inheritance, tagged with the stack.
func Create(ctx context.Context, parent, stack string) (string, error) {
	if stack == "" || strings.ContainsAny(stack, "/@# ") {
		return "", fmt.Errorf("not a dataset name: %q", stack)
	}
	name := parent + "/" + stack
	if _, err := run(ctx, "create", "-o", Tag+"="+stack, name); err != nil {
		return "", fmt.Errorf("zfs create %s: %w", name, err)
	}
	return name, nil
}

// Owned says whether name and every dataset under it carry stack's tag. A
// child fjord did not make is named in the error, so it is never destroyed
// along with the app.
func Owned(ctx context.Context, name, stack string) error {
	out, err := run(ctx, "list", "-H", "-r", "-o", "name,"+Tag, "-t", "filesystem,volume", name)
	if err != nil {
		return fmt.Errorf("zfs list %s: %w", name, err)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 2 {
			continue
		}
		if f[1] != stack {
			if f[0] == name {
				return fmt.Errorf("%s wasn't made by fjord for %s", name, stack)
			}
			return fmt.Errorf("%s wasn't made by fjord; remove it first", f[0])
		}
		found = found || f[0] == name
	}
	if !found {
		return fmt.Errorf("%s not found", name)
	}
	return nil
}

// Destroy removes name with its snapshots and children, only when Owned
// says all of it is stack's.
func Destroy(ctx context.Context, name, stack string) error {
	if err := Owned(ctx, name, stack); err != nil {
		return err
	}
	if _, err := run(ctx, "destroy", "-r", name); err != nil {
		return fmt.Errorf("zfs destroy %s: %w", name, err)
	}
	return nil
}
