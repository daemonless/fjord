package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/daemonless/fjord/pkg/dataset"
	"github.com/daemonless/fjord/pkg/history"
	"github.com/daemonless/fjord/pkg/stack"
)

type migratePreview struct {
	Eligible       bool   `json:"eligible"`
	Reason         string `json:"reason,omitempty"`
	HasZFS         bool   `json:"hasZfs"`
	SourceDir      string `json:"sourceDir,omitempty"`
	TargetLocation string `json:"targetLocation,omitempty"`
	TargetDataset  string `json:"targetDataset,omitempty"`
	SizeBytes      int64  `json:"sizeBytes"`
	NeededBytes    int64  `json:"neededBytes"`
	FreeBytes      int64  `json:"freeBytes"`
	EstSeconds     int    `json:"estSeconds"`
	ZFSCreateHint  string `json:"zfsCreateHint,omitempty"`
}

type storageLocationInfo struct {
	Path          string `json:"path"`
	Dataset       string `json:"dataset,omitempty"`
	IsDataset     bool   `json:"isDataset"`
	ZFSCreateHint string `json:"zfsCreateHint,omitempty"`
}

type storageStackInfo struct {
	Name          string `json:"name"`
	DisplayName   string `json:"displayName"`
	Dataset       string `json:"dataset,omitempty"`
	PlainFolder   string `json:"plainFolder,omitempty"`
	Eligible      bool   `json:"eligible"`
	Reason        string `json:"reason,omitempty"`
	TargetDataset string `json:"targetDataset,omitempty"`
	SizeBytes     int64  `json:"sizeBytes"`
}

type storageDatasetsResponse struct {
	HasZFS        bool                  `json:"hasZfs"`
	ZFSCreateHint string                `json:"zfsCreateHint,omitempty"`
	Locations     []storageLocationInfo `json:"locations"`
	Stacks        []storageStackInfo    `json:"stacks"`
}

func zfsCreateHintForHost(ctx context.Context) string {
	pool := dataset.FirstPool(ctx)
	if pool == "" {
		pool = "zroot"
	}
	return fmt.Sprintf("zfs create -o mountpoint=/apps %s/apps", pool)
}

// ownDataDirs returns app-data directories named after st.Name that st binds
// into and no other stack shares.
func (s *server) ownDataDirs(st *stack.Stack) []string {
	mounts := stackMounts(st)
	others := s.otherStacksBinds(st.Name)
	var out []string
	seen := map[string]bool{}
	for _, loc := range s.dataLocations() {
		dir := filepath.Join(loc, st.Name)
		if seen[dir] || !isRealDir(dir) || !bindsInside(mounts, dir) || anyInside(others, dir) {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out
}

func (s *server) planMigrateDataset(ctx context.Context, st *stack.Stack) migratePreview {
	p := migratePreview{HasZFS: dataset.HasZFS(ctx)}
	if !p.HasZFS {
		p.Reason = "ZFS is not available on this host"
		return p
	}
	if st.State != nil && st.State.Dataset != "" {
		p.Reason = st.Name + " already uses its own dataset"
		return p
	}
	if ds, ok := dataset.LiveByTag(ctx, st.Name); ok && ds != "" {
		p.Reason = st.Name + " already uses its own dataset (" + ds + ")"
		return p
	}
	dirs := s.ownDataDirs(st)
	if len(dirs) == 0 {
		p.Reason = "No dedicated app data folder found for " + st.Name
		return p
	}
	if len(dirs) > 1 {
		p.Reason = st.Name + " uses multiple app data folders"
		return p
	}
	srcDir := filepath.Clean(dirs[0])
	p.SourceDir = srcDir
	loc := filepath.Dir(srcDir)

	parentDS, ok := dataset.At(ctx, loc)
	if !ok || loc == "/" || dataset.IsBootEnv(parentDS) {
		p.ZFSCreateHint = zfsCreateHintForHost(ctx)
		p.Reason = loc + " sits on the OS root filesystem instead of its own ZFS dataset"
		return p
	}

	p.TargetLocation = loc
	p.TargetDataset = parentDS + "/" + st.Name

	sizeBytes, _ := dirSize(srcDir, 3*time.Second)
	p.SizeBytes = sizeBytes
	p.NeededBytes = int64(float64(sizeBytes)*1.2) + 16*1024*1024
	if free, err := dataset.AvailableBytes(ctx, parentDS); err == nil {
		p.FreeBytes = free
	}
	// Conservative copy rate (~100 MB/s) plus ~10s stop/start overhead.
	est := int(sizeBytes/(100*1024*1024)) + 10
	if est < 10 {
		est = 10
	}
	p.EstSeconds = est

	if p.FreeBytes > 0 && p.FreeBytes < p.NeededBytes {
		p.Reason = fmt.Sprintf("Not enough free space on %s (%s needed, %s free)", parentDS, formatBytes(p.NeededBytes), formatBytes(p.FreeBytes))
		return p
	}

	p.Eligible = true
	return p
}

func (s *server) stackMigrateDatasetPreview(w http.ResponseWriter, r *http.Request, name string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.planMigrateDataset(ctx, st))
}

// copyTreePreserving copies all files from srcDir into dstDir using tar with
// --numeric-owner and sparse/xattr/ACL flags appropriate to the host OS.
func copyTreePreserving(ctx context.Context, srcDir, dstDir string) error {
	var packArgs, unpackArgs []string
	if runtime.GOOS == "linux" {
		packArgs = []string{"--numeric-owner", "--sparse", "--xattrs", "--acls", "-cf", "-", "-C", srcDir, "."}
		unpackArgs = []string{"--numeric-owner", "--sparse", "--xattrs", "--acls", "-xpf", "-", "-C", dstDir}
	} else {
		packArgs = []string{"--numeric-owner", "-S", "-cf", "-", "-C", srcDir, "."}
		unpackArgs = []string{"--numeric-owner", "-S", "-xpf", "-", "-C", dstDir}
	}

	pack := exec.CommandContext(ctx, "tar", packArgs...)
	unpack := exec.CommandContext(ctx, "tar", unpackArgs...)
	var packErr, unpackErr bytes.Buffer
	pack.Stderr = &packErr
	unpack.Stderr = &unpackErr

	pr, pw := io.Pipe()
	pack.Stdout = pw
	unpack.Stdin = pr

	if err := unpack.Start(); err != nil {
		pw.Close()
		pr.Close()
		return fmt.Errorf("start tar extract: %w", err)
	}
	if err := pack.Start(); err != nil {
		pw.Close()
		pr.Close()
		_ = unpack.Wait()
		return fmt.Errorf("start tar create: %w", err)
	}

	pErr := pack.Wait()
	pw.Close()
	uErr := unpack.Wait()
	pr.Close()

	if pErr != nil {
		return fmt.Errorf("tar create: %v (%s)", pErr, strings.TrimSpace(packErr.String()))
	}
	if uErr != nil {
		return fmt.Errorf("tar extract: %v (%s)", uErr, strings.TrimSpace(unpackErr.String()))
	}
	return nil
}

func statOwnerMode(path string) (uid, gid int, mode os.FileMode, ok bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fi.Mode().Perm(), false
	}
	return int(st.Uid), int(st.Gid), fi.Mode().Perm(), true
}

// reconcileMigrateDataset recovers an interrupted Move to dataset on boot:
// if the backup folder still exists and the target dataset was only half
// created, destroy the incomplete dataset and rename the original folder back.
func reconcileMigrateDataset(ctx context.Context, stackName, targetDataset, backupFolder string) string {
	if backupFolder == "" {
		return ""
	}
	if _, err := os.Stat(backupFolder); err != nil {
		return ""
	}
	origDir := backupFolder
	if idx := strings.Index(backupFolder, ".pre-dataset-"); idx > 0 {
		origDir = backupFolder[:idx]
	}
	if targetDataset != "" && dataset.Exists(ctx, targetDataset) {
		_ = dataset.Destroy(ctx, targetDataset, stackName)
	}
	if _, err := os.Stat(origDir); os.IsNotExist(err) {
		if err := os.Rename(backupFolder, origDir); err == nil {
			return "restored original folder from interrupted dataset migration"
		}
	}
	return ""
}

// stackMigrateDataset handles POST /api/stacks/<name>/migrate-dataset.
// Body: {"keepOld": true}
func (s *server) stackMigrateDataset(w http.ResponseWriter, r *http.Request, name string) {
	var req struct {
		KeepOld *bool `json:"keepOld"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	keepOld := true
	if req.KeepOld != nil {
		keepOld = *req.KeepOld
	}

	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}

	unlock, ok := lockStackAs(name, "migrate")
	if !ok {
		http.Error(w, anotherOperation(name), http.StatusConflict)
		return
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
	defer cancel()

	plan := s.planMigrateDataset(ctx, st)
	if !plan.Eligible {
		http.Error(w, plan.Reason, http.StatusConflict)
		return
	}

	parentDS, ok := dataset.At(ctx, plan.TargetLocation)
	if !ok || dataset.IsBootEnv(parentDS) {
		http.Error(w, plan.TargetLocation+" is not a ZFS dataset", http.StatusConflict)
		return
	}

	_, imagesJSON := currentStackVersionImage(ctx, s, st)
	uid, gid, mode, hasOwner := statOwnerMode(plan.SourceDir)

	be := s.backendFor(st)
	if err := stopStackForZFS(ctx, be, st); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	wasRunning := st.State != nil && st.State.DesiredState == "running"
	now := time.Now().UTC()
	backupDir := plan.SourceDir + ".pre-dataset-" + now.Format("20060102-150405")

	_ = s.manager.UpdateState(name, func(state *stack.State) *stack.State {
		if state != nil {
			state.PendingDatasetOp = &stack.PendingDatasetOp{
				Kind:        "migrate_dataset",
				LiveDataset: plan.TargetDataset,
				KeptDataset: backupDir,
				StartedAt:   now,
			}
		}
		return state
	})

	rollbackMigration := func(dsCreated bool, reason error) {
		if dsCreated {
			_ = dataset.Destroy(ctx, plan.TargetDataset, name)
		}
		_ = os.Rename(backupDir, plan.SourceDir)
		_ = s.manager.UpdateState(name, func(state *stack.State) *stack.State {
			if state != nil {
				state.Dataset = ""
				state.PendingDatasetOp = nil
			}
			return state
		})
		if wasRunning {
			if stream, uerr := be.Up(ctx, st); uerr == nil {
				io.Copy(io.Discard, stream)
				stream.Close()
			}
		}
		http.Error(w, reason.Error(), http.StatusConflict)
	}

	if err := os.Rename(plan.SourceDir, backupDir); err != nil {
		rollbackMigration(false, fmt.Errorf("rename %s: %w", plan.SourceDir, err))
		return
	}

	createdDS, err := dataset.Create(ctx, parentDS, name)
	if err != nil {
		rollbackMigration(false, err)
		return
	}

	if hasOwner {
		if err := os.Chown(plan.SourceDir, uid, gid); err != nil {
			log.Printf("migrate %s: chown %s: %v", name, plan.SourceDir, err)
		}
		if err := os.Chmod(plan.SourceDir, mode); err != nil {
			log.Printf("migrate %s: chmod %s: %v", name, plan.SourceDir, err)
		}
	}

	if err := copyTreePreserving(ctx, backupDir, plan.SourceDir); err != nil {
		rollbackMigration(true, err)
		return
	}

	_, _ = dataset.SnapshotManual(ctx, createdDS, name, "Moved to dataset", now, imagesJSON)

	_ = s.manager.UpdateState(name, func(state *stack.State) *stack.State {
		if state != nil {
			state.Dataset = createdDS
			state.PendingDatasetOp = nil
		}
		return state
	})

	if fresh, gerr := s.manager.Get(name); gerr == nil {
		st = fresh
	}
	if wasRunning {
		stream, uerr := be.Up(ctx, st)
		if uerr != nil {
			rollbackMigration(true, fmt.Errorf("restart after migration: %w", uerr))
			return
		}
		io.Copy(io.Discard, stream)
		stream.Close()
	}

	if !keepOld {
		_ = os.RemoveAll(backupDir)
		backupDir = ""
	}

	n := newNote(history.Snapshot, triggerOf(r))
	n.detail = "Moved to dataset " + createdDS
	s.writeHistory(name, n, false, "")
	s.fleet.forget(name)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":        true,
		"dataset":   createdDS,
		"backupDir": backupDir,
	})
}

func (s *server) handleStorageDatasets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	hasZFS := dataset.HasZFS(ctx)
	resp := storageDatasetsResponse{
		HasZFS:    hasZFS,
		Locations: []storageLocationInfo{},
		Stacks:    []storageStackInfo{},
	}
	if hasZFS {
		resp.ZFSCreateHint = zfsCreateHintForHost(ctx)
	}

	for _, loc := range s.appDataLocations() {
		info := storageLocationInfo{Path: loc}
		if hasZFS {
			if ds, ok := dataset.At(ctx, loc); ok && loc != "/" && !dataset.IsBootEnv(ds) {
				info.Dataset = ds
				info.IsDataset = true
			} else {
				info.ZFSCreateHint = resp.ZFSCreateHint
			}
		}
		resp.Locations = append(resp.Locations, info)
	}

	stacks, err := s.manager.List()
	if err == nil {
		for _, st := range stacks {
			full, gerr := s.manager.Get(st.Name)
			if gerr != nil {
				continue
			}
			ds := ""
			if full.State != nil {
				ds = full.State.Dataset
			}
			if ds == "" && hasZFS {
				if live, ok := dataset.LiveByTag(ctx, full.Name); ok {
					ds = live
				}
			}
			item := storageStackInfo{
				Name:        full.Name,
				DisplayName: full.DisplayName,
				Dataset:     ds,
			}
			if ds == "" && hasZFS {
				plan := s.planMigrateDataset(ctx, full)
				item.PlainFolder = plan.SourceDir
				item.Eligible = plan.Eligible
				item.Reason = plan.Reason
				item.TargetDataset = plan.TargetDataset
				item.SizeBytes = plan.SizeBytes
			}
			resp.Stacks = append(resp.Stacks, item)
		}
		sort.Slice(resp.Stacks, func(i, j int) bool { return resp.Stacks[i].Name < resp.Stacks[j].Name })
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
