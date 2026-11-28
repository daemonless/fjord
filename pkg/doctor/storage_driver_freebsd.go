//go:build freebsd

package doctor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const podmanStorageConf = "/usr/local/etc/containers/storage.conf"

// storageDriverCommands are what the Fix does, shown before it runs.
var storageDriverCommands = []string{
	`sed -i .fjord-bak 's/^driver = "zfs"/driver = "vfs"/' ` + podmanStorageConf,
	"mv /var/db/containers/storage /var/db/containers/storage.fjord-zfs",
	"service podman_service onerestart",
}

// storageDriverCheck: pkg's podman stores images with its zfs driver, which
// needs the storage folder on a ZFS dataset. On a host without ZFS podman
// does not start at all, and fjord sits on an engine that never answers.
func storageDriverCheck() Check {
	return Check{
		ID: "podman-storage", Name: "podman image storage fits this disk", Engine: "podman", HostOnly: true,
		Probe:    func(context.Context) (Status, string) { return storageDriverStatus(podmanStorageConf, fsTypeOf) },
		Install:  setStorageVFS(podmanStorageConf),
		Commands: storageDriverCommands,
		Action:   "Store images with the vfs driver",
		Why:      "podman's FreeBSD package stores images with its zfs driver, which needs its storage folder on a ZFS dataset. On a host without ZFS podman cannot start at all. The vfs driver works on any disk; it keeps a full copy of each image layer, so images take more space.",
		Fix:      strings.Join(storageDriverCommands, "\n"),
	}
}

var driverLine = regexp.MustCompile(`(?m)^\s*driver\s*=\s*"([^"]*)"`)

// storageDriver reads the driver from a storage.conf body ("" when unset).
func storageDriver(conf string) string {
	if m := driverLine.FindStringSubmatch(conf); m != nil {
		return m[1]
	}
	return ""
}

// storageDriverStatus fails only for the one case that stops podman: the
// zfs driver with its folder (or, before first use, the nearest folder
// that exists) on another filesystem.
func storageDriverStatus(confPath string, fsType func(string) (string, error)) (Status, string) {
	b, err := os.ReadFile(confPath)
	if err != nil {
		return OK, "no storage.conf; podman's defaults apply"
	}
	conf := string(b)
	driver := storageDriver(conf)
	root := parseGraphRoot(conf)
	if root == "" {
		root = "/var/db/containers/storage"
	}
	if driver != "zfs" {
		// A folder podman made for zfs keeps zfs in its database, which wins
		// over the setting: podman still cannot start.
		if _, err := os.Stat(filepath.Join(root, "zfs")); err == nil {
			if fs, err := fsType(root); err == nil && fs != "zfs" {
				return Fail, fmt.Sprintf("%s was made for the zfs driver and is on %s: podman cannot start until it is moved aside", root, fs)
			}
		}
		if driver == "" {
			driver = "podman's default"
		}
		return OK, "driver " + driver
	}
	dir := root
	for {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	fs, err := fsType(dir)
	if err != nil {
		return Warn, fmt.Sprintf("driver zfs; could not tell what %s is on: %v", dir, err)
	}
	if fs == "zfs" {
		return OK, "driver zfs, " + root + " on ZFS"
	}
	return Fail, fmt.Sprintf("driver zfs, but %s is on %s, not ZFS: podman cannot start", root, fs)
}

// fsTypeOf names the filesystem path is on ("zfs", "ufs", ...).
func fsTypeOf(path string) (string, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", err
	}
	var b []byte
	for _, c := range st.Fstypename {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b), nil
}

// setStorageVFS switches the driver to vfs, moves aside the storage folder
// podman made on its failed first start (its database still names zfs and
// overrides the setting), then restarts podman's API service.
func setStorageVFS(confPath string) func(context.Context, io.Writer) error {
	return func(ctx context.Context, w io.Writer) error {
		b, err := os.ReadFile(confPath)
		if err != nil {
			return err
		}
		root := parseGraphRoot(string(b))
		if root == "" {
			root = "/var/db/containers/storage"
		}
		if err := useVFS(confPath, w); err != nil {
			return err
		}
		if err := moveFailedStore(root, fsTypeOf, w); err != nil {
			return err
		}
		return restartService("podman_service")(ctx, w)
	}
}

// moveFailedStore moves root aside when it is not on ZFS: there the zfs
// driver never started, so it holds no images or volumes, only a database
// that would put the zfs driver back. Moved, not deleted.
func moveFailedStore(root string, fsType func(string) (string, error), w io.Writer) error {
	if _, err := os.Stat(root); err != nil {
		return nil // not made yet
	}
	if fs, err := fsType(root); err != nil || fs == "zfs" {
		return err
	}
	dest := root + ".fjord-zfs"
	if _, err := os.Stat(dest); err == nil {
		dest += "-" + time.Now().Format("20060102-150405")
	}
	if err := os.Rename(root, dest); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s moved to %s: podman made it on a start that failed\n", root, dest)
	return nil
}

// useVFS rewrites a zfs driver line to vfs, the old file kept as
// <conf>.fjord-bak. Any other driver is left alone.
func useVFS(confPath string, w io.Writer) error {
	b, err := os.ReadFile(confPath)
	if err != nil {
		return err
	}
	conf := string(b)
	if storageDriver(conf) != "zfs" {
		fmt.Fprintf(w, "%s already uses driver %q\n", confPath, storageDriver(conf))
		return nil
	}
	if err := os.WriteFile(confPath+".fjord-bak", b, 0o644); err != nil {
		return err
	}
	tmp := confPath + ".fjord-new"
	if err := os.WriteFile(tmp, []byte(driverLine.ReplaceAllString(conf, `driver = "vfs"`)), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, confPath); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s: driver = \"vfs\" (the old file is %s.fjord-bak)\n", confPath, confPath)
	return nil
}
