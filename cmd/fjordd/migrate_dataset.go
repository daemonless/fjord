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
	TargetDir      string `json:"targetDir,omitempty"`
	TargetDataset  string `json:"targetDataset,omitempty"`
	CrossLocation  bool   `json:"crossLocation,omitempty"`
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
	TargetDir     string `json:"targetDir,omitempty"`
	TargetDataset string `json:"targetDataset,omitempty"`
	CrossLocation bool   `json:"crossLocation,omitempty"`
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

func isPathPrefixPrev(b byte) bool {
	switch b {
	case '=', ':', '"', '\'', ' ', '\t', '-', '\n', '\r', '(', '[', '{', ',':
		return true
	}
	return false
}

func isPathPrefixNext(b byte) bool {
	switch b {
	case '/', ':', '"', '\'', ' ', '\t', '\n', '\r', ')', ']', '}', ',', ';':
		return true
	}
	return false
}

// rewritePathPrefix replaces oldDir with newDir only on clean path boundaries
// so /containers/app matches /containers/app and /containers/app/config, never
// /containers/app2 or /other/containers/app.
func rewritePathPrefix(text, oldDir, newDir string) (string, bool) {
	oldDir = filepath.Clean(oldDir)
	newDir = filepath.Clean(newDir)
	if oldDir == "" || oldDir == "/" || oldDir == newDir || !strings.Contains(text, oldDir) {
		return text, false
	}
	var out strings.Builder
	out.Grow(len(text))
	changed := false
	i := 0
	for i < len(text) {
		idx := strings.Index(text[i:], oldDir)
		if idx < 0 {
			out.WriteString(text[i:])
			break
		}
		pos := i + idx
		end := pos + len(oldDir)
		prevOK := pos == 0 || isPathPrefixPrev(text[pos-1])
		nextOK := end == len(text) || isPathPrefixNext(text[end])
		out.WriteString(text[i:pos])
		if prevOK && nextOK {
			out.WriteString(newDir)
			changed = true
		} else {
			out.WriteString(oldDir)
		}
		i = end
	}
	return out.String(), changed
}

// firstDatasetLocation returns the highest-priority configured App data
// location backed by a dedicated non-boot-env ZFS dataset.
func (s *server) firstDatasetLocation(ctx context.Context) (loc, ds string, ok bool) {
	for _, cand := range s.appDataLocations() {
		cand = filepath.Clean(cand)
		if cand == "/" {
			continue
		}
		if candDS, found := dataset.At(ctx, cand); found && !dataset.IsBootEnv(candDS) {
			return cand, candDS, true
		}
	}
	return "", "", false
}

// stackHasRewritablePath reports whether st's compose, .env, or AppJail
// director bundle references srcDir on a clean path boundary.
func stackHasRewritablePath(st *stack.Stack, srcDir string) bool {
	if _, ok := rewritePathPrefix(st.Compose, srcDir, srcDir+".check"); ok {
		return true
	}
	if _, ok := rewritePathPrefix(st.Env, srcDir, srcDir+".check"); ok {
		return true
	}
	if st.Dir != "" {
		if b, err := os.ReadFile(filepath.Join(st.Dir, "appjail-director.yml")); err == nil {
			if _, ok := rewritePathPrefix(string(b), srcDir, srcDir+".check"); ok {
				return true
			}
		}
	}
	return false
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
	if ok && loc != "/" && !dataset.IsBootEnv(parentDS) {
		p.TargetLocation = loc
		p.TargetDir = srcDir
		p.TargetDataset = parentDS + "/" + st.Name
	} else {
		candLoc, candDS, hasCand := s.firstDatasetLocation(ctx)
		if !hasCand {
			p.ZFSCreateHint = zfsCreateHintForHost(ctx)
			p.Reason = loc + " sits on the OS root filesystem instead of its own ZFS dataset"
			return p
		}
		targetDir := filepath.Join(candLoc, st.Name)
		targetDS := candDS + "/" + st.Name
		if _, err := os.Lstat(targetDir); err == nil {
			p.Reason = targetDir + " already exists; remove or rename it first so fjord never overwrites existing data"
			return p
		}
		if dataset.Exists(ctx, targetDS) {
			p.Reason = targetDS + " already exists"
			return p
		}
		if !stackHasRewritablePath(st, srcDir) {
			p.Reason = "Could not locate " + srcDir + " in this app's compose or .env to rewrite safely"
			return p
		}
		parentDS = candDS
		p.TargetLocation = candLoc
		p.TargetDir = targetDir
		p.TargetDataset = targetDS
		p.CrossLocation = true
	}

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
// destroys any half-created target dataset and, for same-location migrations
// that renamed the original folder first, renames the original folder back.
func reconcileMigrateDataset(ctx context.Context, stackName, targetDataset, backupFolder string) string {
	if backupFolder == "" {
		if targetDataset != "" && dataset.Exists(ctx, targetDataset) {
			if err := dataset.Destroy(ctx, targetDataset, stackName); err == nil {
				return "cleaned up interrupted dataset migration (original folder untouched)"
			}
		}
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
	dstDir := plan.TargetDir
	if dstDir == "" {
		dstDir = plan.SourceDir
	}

	origCompose := st.Compose
	origEnv := st.Env
	directorPath := ""
	var origDirector []byte
	directorRewritten := false
	if plan.CrossLocation && st.Dir != "" {
		cand := filepath.Join(st.Dir, "appjail-director.yml")
		if b, rerr := os.ReadFile(cand); rerr == nil {
			directorPath = cand
			origDirector = b
		}
	}

	pendingKept := backupDir
	if plan.CrossLocation {
		pendingKept = "" // original folder stays untouched until the stack is running on the new dataset
	}
	_ = s.manager.UpdateState(name, func(state *stack.State) *stack.State {
		if state != nil {
			state.PendingDatasetOp = &stack.PendingDatasetOp{
				Kind:        "migrate_dataset",
				LiveDataset: plan.TargetDataset,
				KeptDataset: pendingKept,
				StartedAt:   now,
			}
		}
		return state
	})

	configRewritten := false
	rollbackMigration := func(dsCreated bool, reason error) {
		if configRewritten {
			_, _ = s.manager.Update(name, func(cur *stack.Stack) error {
				cur.Compose = origCompose
				cur.Env = origEnv
				return nil
			})
		}
		if directorRewritten && directorPath != "" {
			_ = os.WriteFile(directorPath, origDirector, 0o644)
		}
		if dsCreated {
			_ = dataset.Destroy(ctx, plan.TargetDataset, name)
		}
		if !plan.CrossLocation {
			_ = os.Rename(backupDir, plan.SourceDir)
		}
		_ = s.manager.UpdateState(name, func(state *stack.State) *stack.State {
			if state != nil {
				state.Dataset = ""
				state.PendingDatasetOp = nil
			}
			return state
		})
		if fresh, gerr := s.manager.Get(name); gerr == nil {
			st = fresh
		}
		if wasRunning {
			if stream, uerr := be.Up(ctx, st); uerr == nil {
				io.Copy(io.Discard, stream)
				stream.Close()
			}
		}
		http.Error(w, reason.Error(), http.StatusConflict)
	}

	copySrc := plan.SourceDir
	if !plan.CrossLocation {
		if err := os.Rename(plan.SourceDir, backupDir); err != nil {
			rollbackMigration(false, fmt.Errorf("rename %s: %w", plan.SourceDir, err))
			return
		}
		copySrc = backupDir
	}

	createdDS, err := dataset.Create(ctx, parentDS, name)
	if err != nil {
		rollbackMigration(false, err)
		return
	}

	if fi, serr := os.Stat(dstDir); serr != nil || !fi.IsDir() {
		rollbackMigration(true, fmt.Errorf("target dataset mount %s not available", dstDir))
		return
	}

	if hasOwner {
		if err := os.Chown(dstDir, uid, gid); err != nil {
			log.Printf("migrate %s: chown %s: %v", name, dstDir, err)
		}
		if err := os.Chmod(dstDir, mode); err != nil {
			log.Printf("migrate %s: chmod %s: %v", name, dstDir, err)
		}
	}

	if err := copyTreePreserving(ctx, copySrc, dstDir); err != nil {
		rollbackMigration(true, err)
		return
	}

	if plan.CrossLocation {
		newCompose, cChanged := rewritePathPrefix(origCompose, plan.SourceDir, dstDir)
		newEnv, eChanged := rewritePathPrefix(origEnv, plan.SourceDir, dstDir)
		newDirector := ""
		dChanged := false
		if directorPath != "" {
			newDirector, dChanged = rewritePathPrefix(string(origDirector), plan.SourceDir, dstDir)
		}
		if !cChanged && !eChanged && !dChanged {
			rollbackMigration(true, fmt.Errorf("could not rewrite %s in stack configuration", plan.SourceDir))
			return
		}
		if cChanged || eChanged {
			if _, err := s.manager.Update(name, func(cur *stack.Stack) error {
				cur.Compose = newCompose
				cur.Env = newEnv
				return nil
			}); err != nil {
				rollbackMigration(true, fmt.Errorf("update stack config: %w", err))
				return
			}
			configRewritten = true
		}
		if dChanged && directorPath != "" {
			if err := stack.WriteFileAtomic(directorPath, []byte(newDirector), 0o644); err != nil {
				rollbackMigration(true, fmt.Errorf("update appjail-director.yml: %w", err))
				return
			}
			directorRewritten = true
		}
	}

	_, _ = dataset.SnapshotManual(ctx, createdDS, name, "Moved to dataset", now, imagesJSON)

	rpDef, _, _ := restorePointSettingsOf(loadSettings(s.fjordRoot))
	_ = s.manager.UpdateState(name, func(state *stack.State) *stack.State {
		if state != nil {
			state.Dataset = createdDS
			if state.RestorePointPolicy == "" {
				state.RestorePointPolicy = rpDef
			}
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

	if plan.CrossLocation {
		if keepOld {
			if err := os.Rename(plan.SourceDir, backupDir); err != nil {
				log.Printf("migrate %s: rename backup %s: %v", name, plan.SourceDir, err)
				backupDir = plan.SourceDir
			}
		} else {
			_ = os.RemoveAll(plan.SourceDir)
			backupDir = ""
		}
	} else if !keepOld {
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
		"targetDir": dstDir,
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
				item.TargetDir = plan.TargetDir
				item.TargetDataset = plan.TargetDataset
				item.CrossLocation = plan.CrossLocation
				item.SizeBytes = plan.SizeBytes
			}
			resp.Stacks = append(resp.Stacks, item)
		}
		sort.Slice(resp.Stacks, func(i, j int) bool { return resp.Stacks[i].Name < resp.Stacks[j].Name })
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
