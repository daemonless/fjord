package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/daemonless/fjord/pkg/dataset"
)

func TestCopyTreePreserving(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "data.db"), []byte("hello-fjord"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("data.db", filepath.Join(src, "sub", "link.db")); err != nil {
		t.Fatal(err)
	}

	if err := copyTreePreserving(context.Background(), src, dst); err != nil {
		t.Fatalf("copyTreePreserving: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "sub", "data.db"))
	if err != nil || string(got) != "hello-fjord" {
		t.Fatalf("copied data=%q err=%v", string(got), err)
	}
	fi, err := os.Stat(filepath.Join(dst, "sub", "data.db"))
	if err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode=%v err=%v, want 0640", fi.Mode().Perm(), err)
	}
	target, err := os.Readlink(filepath.Join(dst, "sub", "link.db"))
	if err != nil || target != "data.db" {
		t.Fatalf("symlink=%q err=%v", target, err)
	}
}

func TestReconcileMigrateDatasetRestoresBackupFolder(t *testing.T) {
	root := t.TempDir()
	orig := filepath.Join(root, "syncthing")
	backup := orig + ".pre-dataset-20261127-100000"
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "marker"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	note := reconcileMigrateDataset(context.Background(), "syncthing", "", backup)
	if note == "" {
		t.Fatal("expected recovery note")
	}
	if _, err := os.Stat(filepath.Join(orig, "marker")); err != nil {
		t.Fatalf("original folder not restored: %v", err)
	}
}

func TestIsBootEnv(t *testing.T) {
	if !dataset.IsBootEnv("zroot/ROOT/default") || !dataset.IsBootEnv("zroot/ROOT") {
		t.Fatal("boot env not detected")
	}
	if dataset.IsBootEnv("zroot/containers") || dataset.IsBootEnv("tank/apps") {
		t.Fatal("normal dataset flagged as boot env")
	}
}

func TestRewritePathPrefix(t *testing.T) {
	in := "CONFIG_DATA=/containers/zensical/config\nROOT=/containers/zensical\nOTHER=/containers/zensical2/config\nPREFIX=/mnt/containers/zensical/config\n"
	got, changed := rewritePathPrefix(in, "/containers/zensical", "/apps/zensical")
	if !changed {
		t.Fatal("expected rewritePathPrefix to report changed")
	}
	want := "CONFIG_DATA=/apps/zensical/config\nROOT=/apps/zensical\nOTHER=/containers/zensical2/config\nPREFIX=/mnt/containers/zensical/config\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}

	compose := "services:\n  app:\n    volumes:\n      - \"/containers/zensical/config:/config\"\n      - /containers/zensical:/data:ro\n"
	gotC, changedC := rewritePathPrefix(compose, "/containers/zensical", "/apps/zensical")
	if !changedC {
		t.Fatal("expected compose rewrite")
	}
	wantC := "services:\n  app:\n    volumes:\n      - \"/apps/zensical/config:/config\"\n      - /apps/zensical:/data:ro\n"
	if gotC != wantC {
		t.Fatalf("compose got:\n%s\nwant:\n%s", gotC, wantC)
	}
}
