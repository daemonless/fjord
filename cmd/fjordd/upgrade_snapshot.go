package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// keepSnapshots is how many upgrade snapshots stay in <root>/backups.
const keepSnapshots = 3

// versionFile records the version that last ran on this fjord root.
const versionFile = ".version"

// snapshotSkip are root entries never copied: the catalog is a cache fetched
// again on the next refresh, backups would copy itself, and containers is
// the default app-data folder -- the apps' own data, gigabytes, and not
// fjord's to snapshot.
var snapshotSkip = map[string]bool{"catalog": true, "backups": true, "containers": true}

// appDataInRoot is every app-data location from settings.json that sits
// directly in the root, by entry name: those hold app data too. Read raw:
// loadSettings migrates old fields and may write the file back, and nothing
// may change before the snapshot is taken.
func appDataInRoot(root string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(root, "settings.json"))
	if err != nil {
		return out
	}
	var st struct {
		AppData     []string `json:"appData"`
		StorageBase string   `json:"storageBase"`
	}
	if json.Unmarshal(b, &st) != nil {
		return out
	}
	for _, loc := range append(st.AppData, st.StorageBase) {
		if rel, err := filepath.Rel(root, loc); err == nil && loc != "" && !strings.HasPrefix(rel, "..") && rel != "." {
			out[strings.SplitN(rel, string(filepath.Separator), 2)[0]] = true
		}
	}
	return out
}

// snapshotOnUpgrade copies fjord's own state -- stacks, settings, volumes --
// to <root>/backups/ when fjordd starts on a different version than last
// time, and returns where it went ("" when nothing was copied).
//
// Upgrading was one-way: a new version rewrites composes (it pins addresses
// at start) and state files, and going back meant restoring by hand; the
// 0.2 -> 0.3 test copied /var/db/fjord first, by hand. App data under the
// app-data folders is the operator's and is never touched or copied here.
//
// The version is recorded only after the copy succeeds, so a failed copy
// (a full disk) is tried again on the next start.
func snapshotOnUpgrade(root, current string, now time.Time) (string, error) {
	prev, err := os.ReadFile(filepath.Join(root, versionFile))
	last := strings.TrimSpace(string(prev))
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if last == current {
		return "", nil
	}
	if last == "" {
		// No record: a fresh install, or a version from before fjord kept one
		// (0.2 and earlier). Only the second has anything to keep.
		if !hasState(root) {
			return "", writeVersion(root, current)
		}
		last = "unknown"
	}
	name := now.Format("20060102-150405") + "-from-" + safeName(last)
	dst := filepath.Join(root, "backups", name)
	tmp := dst + ".partial"
	os.RemoveAll(tmp) // a copy an earlier start could not finish
	skip := appDataInRoot(root)
	for k := range snapshotSkip {
		skip[k] = true
	}
	if err := copyTree(root, tmp, skip); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	pruneSnapshots(filepath.Join(root, "backups"), keepSnapshots)
	return dst, writeVersion(root, current)
}

// hasState says whether a fjord root holds anything an upgrade could lose.
func hasState(root string) bool {
	if _, err := os.Stat(filepath.Join(root, "settings.json")); err == nil {
		return true
	}
	entries, _ := os.ReadDir(filepath.Join(root, "stacks"))
	return len(entries) > 0
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// safeName makes a version string usable as part of a directory name.
func safeName(v string) string { return unsafeChars.ReplaceAllString(v, "_") }

func writeVersion(root, v string) error {
	tmp := filepath.Join(root, versionFile+".tmp")
	if err := os.WriteFile(tmp, []byte(v+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(root, versionFile))
}

// copyTree copies src into a new directory dst, keeping modes and symlinks,
// and leaving out top-level entries named in skip.
func copyTree(src, dst string, skip map[string]bool) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if top := strings.SplitN(rel, string(filepath.Separator), 2)[0]; skip[top] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case !info.Mode().IsRegular():
			return nil // sockets, fifos: nothing to keep
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// pruneSnapshots keeps the newest keep snapshots. Names start with their
// time, so name order is age order.
func pruneSnapshots(dir string, keep int) {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasSuffix(e.Name(), ".partial") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		os.RemoveAll(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// snapshotNote is the log line for a snapshot, saying how to go back.
func snapshotNote(dst, current string) string {
	return fmt.Sprintf("upgraded to %s: fjord's state was saved to %s first. To go back: stop fjordd, "+
		"reinstall the old version, and copy that folder's contents back over %s", current, dst, filepath.Dir(filepath.Dir(dst)))
}
