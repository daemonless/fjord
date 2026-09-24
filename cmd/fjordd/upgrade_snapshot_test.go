package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A 0.2 root: stacks and settings, no version record -- what every host
// upgrading to the first version with snapshots looks like.
func oldRoot(t *testing.T) string {
	root := t.TempDir()
	write(t, filepath.Join(root, "stacks/notes/compose.yaml"), "services: {}\n")
	write(t, filepath.Join(root, "settings.json"), `{"appData":["`+filepath.Join(root, "apps")+`"]}`)
	write(t, filepath.Join(root, "catalog/daemonless/catalog.json"), "{}")
	write(t, filepath.Join(root, "containers/notes/db/ibdata1"), "app data")
	write(t, filepath.Join(root, "apps/tautulli/config.ini"), "app data")
	write(t, filepath.Join(root, "smb/cred"), "secret")
	return root
}

var t0 = time.Date(2026, 9, 24, 21, 0, 0, 0, time.UTC)

func TestSnapshotFreshInstallCopiesNothing(t *testing.T) {
	root := t.TempDir()
	if dst, err := snapshotOnUpgrade(root, "0.3.0", t0); err != nil || dst != "" {
		t.Fatalf("fresh install: %q, %v", dst, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, versionFile)); strings.TrimSpace(string(b)) != "0.3.0" {
		t.Errorf("version not recorded: %q", b)
	}
}

// fjord's state is copied; app data and the catalog cache are not.
func TestSnapshotFromUnrecordedVersion(t *testing.T) {
	root := oldRoot(t)
	dst, err := snapshotOnUpgrade(root, "0.3.0", t0)
	if err != nil || !strings.HasSuffix(dst, "backups/20260924-210000-from-unknown") {
		t.Fatalf("got %q, %v", dst, err)
	}
	for _, kept := range []string{"stacks/notes/compose.yaml", "settings.json", "smb/cred"} {
		if _, err := os.Stat(filepath.Join(dst, kept)); err != nil {
			t.Errorf("%s not in the snapshot", kept)
		}
	}
	if info, err := os.Stat(filepath.Join(dst, "smb/cred")); err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf("smb/cred mode %v: credentials must stay root-only", info.Mode().Perm())
	}
	for _, left := range []string{"catalog", "containers", "apps", "backups"} {
		if _, err := os.Stat(filepath.Join(dst, left)); err == nil {
			t.Errorf("%s was copied: app data and caches stay out", left)
		}
	}
}

func TestSnapshotOnlyWhenTheVersionChanges(t *testing.T) {
	root := oldRoot(t)
	if _, err := snapshotOnUpgrade(root, "0.3.0", t0); err != nil {
		t.Fatal(err)
	}
	if dst, _ := snapshotOnUpgrade(root, "0.3.0", t0.Add(time.Hour)); dst != "" {
		t.Errorf("a restart on the same version copied again: %s", dst)
	}
	dst, _ := snapshotOnUpgrade(root, "0.4.0", t0.Add(2*time.Hour))
	if !strings.HasSuffix(dst, "-from-0.3.0") {
		t.Errorf("0.3.0 -> 0.4.0 went to %q", dst)
	}
}

func TestSnapshotKeepsThree(t *testing.T) {
	root := oldRoot(t)
	for i, v := range []string{"0.3.0", "0.3.1", "0.3.2", "0.4.0", "0.4.1"} {
		if _, err := snapshotOnUpgrade(root, v, t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(root, "backups"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 3 || !strings.HasSuffix(names[2], "-from-0.4.0") {
		t.Errorf("kept %v, want the newest 3", names)
	}
}
