package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// MkdirOwned creates path as root would for an app, owned by uid:gid --
// only the levels it makes; an existing folder is left as it is. created
// says whether path itself was made.
//
// It follows no symlink an unprivileged user could have planted: a link in
// a folder not owned by root, or one anyone can write to. A container runs
// as uid 1000 and owns its data folder; a link it leaves there pointing at
// /etc made fjord create and chown a folder there as root (review 1.3). A
// system link (FreeBSD's /home -> /usr/home, in /) is followed. A path with
// ".." in it is refused: a prefix check upstream passed it lexically.
func MkdirOwned(path string, mode os.FileMode, uid, gid int) (created bool, err error) {
	if !filepath.IsAbs(path) {
		return false, fmt.Errorf("%s is not an absolute path", path)
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return false, fmt.Errorf("%s climbs out with ..", path)
		}
	}
	path = filepath.Clean(path)
	// Walk down from /, checking each level that exists.
	var missing []string
	cur := "/"
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		next := filepath.Join(cur, part)
		fi, err := os.Lstat(next)
		if os.IsNotExist(err) {
			missing = append(missing, next)
			cur = next
			continue
		}
		if err != nil {
			return false, err
		}
		if len(missing) > 0 {
			// Something appeared below a missing level: someone else is making it.
			return false, fmt.Errorf("%s changed while being made", path)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if !trustedDir(cur) {
				return false, fmt.Errorf("%s goes through the symlink %s, in a folder that is not root's: create it by hand", path, next)
			}
			target, err := filepath.EvalSymlinks(next)
			if err != nil {
				// Dangling: what it names is not made through it.
				return false, fmt.Errorf("%s goes through the dangling symlink %s", path, next)
			}
			next = target
		} else if !fi.IsDir() {
			return false, fmt.Errorf("%s is not a folder", next)
		}
		cur = next
	}
	if len(missing) == 0 {
		return false, nil
	}
	// The real place, links resolved: create the missing levels there.
	real := cur
	for i := len(missing) - 1; i >= 0; i-- {
		real = filepath.Dir(real)
	}
	for _, m := range missing {
		real = filepath.Join(real, filepath.Base(m))
		if err := os.Mkdir(real, mode); err != nil {
			return false, fmt.Errorf("mkdir %s: %w", real, err)
		}
		if err := os.Lchown(real, uid, gid); err != nil {
			return true, fmt.Errorf("chown %s: %w", real, err)
		}
	}
	return true, nil
}

// trustedDir: owned by root and writable by no one else, so a symlink in it
// was put there by root.
func trustedDir(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Uid == 0 && fi.Mode().Perm()&0o022 == 0
}
