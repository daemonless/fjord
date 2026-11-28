package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/dataset"
	"github.com/daemonless/fjord/pkg/history"
	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/stack"
)

// snapshotBeforeUpdateFn and pruneAutomaticSnapshotsFn are the dataset hooks
// snapshotBeforeUpdate calls; tests replace them.
var (
	snapshotBeforeUpdateFn    = dataset.Snapshot
	pruneAutomaticSnapshotsFn = dataset.PruneAutomaticSnapshots
)

// snapshotBeforeUpdate takes an automatic pre-update ZFS restore point of a
// stack's own dataset and records it in state.json. Plain-folder stacks and
// stacks whose restore point policy is "off" are skipped. Returns an error
// when snapshotting an eligible stack fails so automatic updates abort safely.
func (s *server) snapshotBeforeUpdate(ctx context.Context, st *stack.Stack) (string, error) {
	if st == nil || st.State == nil || st.State.Dataset == "" {
		return "", nil
	}
	if effectiveRestorePointPolicy(st.State) == rpPolicyOff {
		return "", nil
	}
	snapCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	_, retDays, _ := restorePointSettingsOf(loadSettings(s.fjordRoot))
	now := time.Now()
	_, imagesJSON := currentStackVersionImage(snapCtx, s, st)
	snap, err := snapshotBeforeUpdateFn(snapCtx, st.State.Dataset, now, -1, imagesJSON)
	if err != nil {
		log.Printf("update %s: snapshot %s: %v", st.Name, st.State.Dataset, err)
		return "[fjord] no snapshot of its data before this update: " + err.Error() + "\n", err
	}
	_, _ = pruneAutomaticSnapshotsFn(snapCtx, st.State.Dataset, time.Duration(retDays)*24*time.Hour, now.UTC())
	if err := s.manager.UpdateState(st.Name, func(state *stack.State) *stack.State {
		if state == nil {
			return nil
		}
		state.UpdateSnapshot = snap
		return state
	}); err != nil {
		log.Printf("update %s: record snapshot: %v", st.Name, err)
	}
	return "[fjord] data snapshot saved: " + snap + "\n", nil
}

// reconcilePendingDatasetOp recovers a stack whose ZFS rename/clone/promote
// was interrupted by a daemon crash or power loss. Runs before start-on-boot
// so no container ever starts on an empty directory.
func reconcilePendingDatasetOp(srv *server, name string) {
	st, err := srv.manager.Get(name)
	if err != nil || st.State == nil || st.State.PendingDatasetOp == nil {
		return
	}
	op := st.State.PendingDatasetOp
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var note string
	if op.Kind == "migrate_dataset" {
		note = reconcileMigrateDataset(ctx, name, op.LiveDataset, op.KeptDataset)
	} else {
		note = dataset.ReconcilePending(ctx, op.LiveDataset, op.KeptDataset, op.NewKeptDataset, op.DropDataset, op.LocalMP)
	}
	if note != "" {
		log.Printf("start-on-boot: %s: %s", name, note)
		n := newNote(history.Rollback, history.ByBoot)
		n.detail = note
		srv.writeHistory(name, n, false, "")
	}
	_ = srv.manager.UpdateState(name, func(state *stack.State) *stack.State {
		if state == nil {
			return nil
		}
		state.PendingDatasetOp = nil
		return state
	})
}

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
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := os.Lstat(own); err == nil {
		// Already there (data being adopted): left as it is. A dataset fjord
		// made for an earlier install under this name is the app's own
		// again, restore points and all.
		return keptDatasetAt(ctx, own, slug)
	}
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
