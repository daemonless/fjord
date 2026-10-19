package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// Folders fjord makes and chowns as root (review 1.3): never through a
// symlink an unprivileged user could have planted (a container owns its data
// folder), never with ".." in the path, and only the levels it made.
func TestMkdirOwned(t *testing.T) {
	root := t.TempDir()
	uid, gid := os.Getuid(), os.Getgid()
	created, err := MkdirOwned(filepath.Join(root, "app", "config"), 0o755, uid, gid)
	if err != nil || !created {
		t.Fatalf("plain: %v %v", created, err)
	}
	if created, err := MkdirOwned(filepath.Join(root, "app", "config"), 0o755, uid, gid); err != nil || created {
		t.Errorf("existing: %v %v", created, err)
	}
	// The app's folder is the container's, as on a host: run as root, the
	// test would otherwise make it root's, whose links are trusted.
	if os.Getuid() == 0 {
		os.Chown(filepath.Join(root, "app"), 1000, 1000)
	}
	outside := t.TempDir()
	// A link in a folder the container owns, pointing out of it.
	os.Symlink(outside, filepath.Join(root, "app", "link"))
	if _, err := MkdirOwned(filepath.Join(root, "app", "link", "etc-like"), 0o755, uid, gid); err == nil {
		t.Error("made a folder through a planted symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "etc-like")); err == nil {
		t.Error("the folder exists where the link points")
	}
	// A dangling link at the leaf is not created through either.
	os.Symlink(filepath.Join(outside, "gone"), filepath.Join(root, "app", "dangling"))
	if created, err := MkdirOwned(filepath.Join(root, "app", "dangling"), 0o755, uid, gid); err == nil && created {
		t.Error("created through a dangling symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "gone")); err == nil {
		t.Error("the dangling link's target was made")
	}
	if _, err := MkdirOwned(root+"/app/../../escape", 0o755, uid, gid); err == nil {
		t.Error("accepted ..")
	}
}
