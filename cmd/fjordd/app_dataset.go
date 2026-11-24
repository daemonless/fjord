package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/dataset"
	"github.com/daemonless/fjord/pkg/manifest"
)

// appDataset gives a new install its own ZFS dataset, <parent>/<slug>, when
// its data location is a dataset's mountpoint and the app keeps data in
// <base>/<slug>: one snapshot then covers the whole app. A plain folder, a
// host without ZFS, or a folder that is already there keeps folders as
// before. Best effort: a dataset that cannot be made leaves the install on
// folders. Returns the dataset's name, or "".
func (s *server) appDataset(ctx context.Context, base, slug string, dirs []manifest.ProvisionDir) string {
	own := filepath.Join(base, slug)
	var first *manifest.ProvisionDir
	for i, d := range dirs {
		if d.Path == own || strings.HasPrefix(d.Path, own+string(filepath.Separator)) {
			first = &dirs[i]
			break
		}
	}
	if first == nil {
		return ""
	}
	if _, err := os.Lstat(own); err == nil {
		return "" // already there (data being adopted): left as it is
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	parent, ok := dataset.At(ctx, base)
	if !ok {
		return ""
	}
	name, err := dataset.Create(ctx, parent, slug)
	if err != nil {
		log.Printf("install %s: no dataset of its own, folders instead: %v", slug, err)
		return ""
	}
	// Owned as the folder would have been: made by Provision, the app's own
	// level went to its user.
	if err := os.Chown(own, first.Uid, first.Gid); err != nil {
		log.Printf("install %s: chown %s: %v", slug, own, err)
	}
	if err := os.Chmod(own, first.Mode); err != nil {
		log.Printf("install %s: chmod %s: %v", slug, own, err)
	}
	log.Printf("install %s: data in its own dataset %s", slug, name)
	return name
}
