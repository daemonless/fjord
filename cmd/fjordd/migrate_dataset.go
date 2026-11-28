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
	"sync"
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

type migrateResult struct {
	Dataset   string `json:"dataset"`
	TargetDir string `json:"targetDir"`
	BackupDir string `json:"backupDir,omitempty"`
}

type migrateAllStatus struct {
	Running  bool              `json:"running"`
	Stopping bool              `json:"stopping,omitempty"`
	Total    int               `json:"total,omitempty"`
	Done     int               `json:"done,omitempty"`
	Current  string            `json:"current,omitempty"`
	Moved    []string          `json:"moved,omitempty"`
	Failed   map[string]string `json:"failed,omitempty"`
	KeepOld  bool              `json:"keepOld,omitempty"`
}

type migrateAllTracker struct {
	mu     sync.Mutex
	status migrateAllStatus
}

func (m *migrateAllTracker) snapshot() *migrateAllStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.status.Running && m.status.Total == 0 {
		return nil
	}
	cp := m.status
	if len(m.status.Moved) > 0 {
		cp.Moved = append([]string(nil), m.status.Moved...)
	}
	if len(m.status.Failed) > 0 {
		cp.Failed = make(map[string]string, len(m.status.Failed))
		for k, v := range m.status.Failed {
			cp.Failed[k] = v
		}
	}
	return &cp
}

func (m *migrateAllTracker) begin(total int, keepOld bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Running {
		return false
	}
	m.status = migrateAllStatus{
		Running: true,
		Total:   total,
		KeepOld: keepOld,
	}
	return true
}

func (m *migrateAllTracker) setTotal(total int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Total = total
}

func (m *migrateAllTracker) setCurrent(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Stopping {
		return false
	}
	m.status.Current = name
	return true
}

func (m *migrateAllTracker) record(name string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Done++
	if err != nil {
		if m.status.Failed == nil {
			m.status.Failed = map[string]string{}
		}
		m.status.Failed[name] = err.Error()
	} else {
		m.status.Moved = append(m.status.Moved, name)
	}
}

func (m *migrateAllTracker) finish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Running = false
	m.status.Stopping = false
	m.status.Current = ""
}

func (m *migrateAllTracker) cancel() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.status.Running {
		return false
	}
	m.status.Stopping = true
	return true
}

type storageDatasetsResponse struct {
	HasZFS        bool                  `json:"hasZfs"`
	ZFSCreateHint string                `json:"zfsCreateHint,omitempty"`
	Locations     []storageLocationInfo `json:"locations"`
	Stacks        []storageStackInfo    `json:"stacks"`
	MigrateAll    *migrateAllStatus     `json:"migrateAll,omitempty"`
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

type progressWriter struct {
	w       io.Writer
	total   int64
	onBytes func(int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	if n > 0 {
		p.total += int64(n)
		if p.onBytes != nil {
			p.onBytes(p.total)
		}
	}
	return n, err
}

// copyTreePreserving copies all files from srcDir into dstDir using tar with
// --numeric-owner and sparse/xattr/ACL flags appropriate to the host OS.
func copyTreePreserving(ctx context.Context, srcDir, dstDir string) error {
	return copyTreePreservingWithProgress(ctx, srcDir, dstDir, nil)
}

func copyTreePreservingWithProgress(ctx context.Context, srcDir, dstDir string, onBytes func(int64)) error {
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
	if onBytes != nil {
		pack.Stdout = &progressWriter{w: pw, onBytes: onBytes}
	} else {
		pack.Stdout = pw
	}
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
				return "A restart interrupted moving data to a dataset; cleaned up the partial dataset and kept the original folder"
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
			return "A restart interrupted moving data to a dataset; put the original folder back"
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
	checkCtx, checkCancel := context.WithTimeout(r.Context(), 15*time.Second)
	plan := s.planMigrateDataset(checkCtx, st)
	checkCancel()
	if !plan.Eligible {
		http.Error(w, plan.Reason, http.StatusConflict)
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
	defer cancel()

	jobID := s.jobs.start("migrate-dataset", "Moving "+name+" to ZFS dataset", false, "Stopping", []bgJobItem{
		{Name: name, Status: "running"},
	})
	res, status, err := s.migrateStackToDataset(ctx, name, keepOld, triggerOf(r), func(phase string, bytesDone, bytesTotal int64) {
		s.jobs.setPhase(jobID, name, phase, bytesDone, bytesTotal)
	})
	if err != nil {
		s.jobs.recordItem(jobID, name, "failed", history.FirstLine(err.Error()))
		s.jobs.finish(jobID, "failed", fmt.Sprintf("Moving %s failed: %s", name, history.FirstLine(err.Error())))
		http.Error(w, err.Error(), status)
		return
	}
	s.jobs.recordItem(jobID, name, "done", res.Dataset)
	s.jobs.finish(jobID, "done", fmt.Sprintf("Moved %s to %s", name, res.Dataset))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":        true,
		"dataset":   res.Dataset,
		"targetDir": res.TargetDir,
		"backupDir": res.BackupDir,
	})
}

// migrateStackToDataset moves one plain-folder stack onto its own ZFS dataset,
// stopping it, copying with permissions preserved, saving an initial restore
// point, and restarting it. On any failure it reverts cleanly and writes a
// failed History entry with a log excerpt.
func (s *server) migrateStackToDataset(ctx context.Context, name string, keepOld bool, trigger string, onProgress func(phase string, bytesDone, bytesTotal int64)) (migrateResult, int, error) {
	report := func(phase string, bytesDone, bytesTotal int64) {
		if onProgress != nil {
			onProgress(phase, bytesDone, bytesTotal)
		}
	}
	st, err := s.manager.Get(name)
	if err != nil {
		return migrateResult{}, http.StatusNotFound, fmt.Errorf("Stack not found")
	}

	unlock, ok := lockStackAs(name, "migrate")
	if !ok {
		return migrateResult{}, http.StatusConflict, fmt.Errorf("%s", anotherOperation(name))
	}
	defer unlock()

	plan := s.planMigrateDataset(ctx, st)
	if !plan.Eligible {
		return migrateResult{}, http.StatusConflict, fmt.Errorf("%s", plan.Reason)
	}

	parentDS, ok := dataset.At(ctx, plan.TargetLocation)
	if !ok || dataset.IsBootEnv(parentDS) {
		return migrateResult{}, http.StatusConflict, fmt.Errorf("%s is not a ZFS dataset", plan.TargetLocation)
	}

	_, imagesJSON := currentStackVersionImage(ctx, s, st)
	uid, gid, mode, hasOwner := statOwnerMode(plan.SourceDir)

	report("Stopping", 0, plan.SizeBytes)
	be := s.backendFor(st)
	if err := stopStackForZFS(ctx, be, st); err != nil {
		return migrateResult{}, http.StatusConflict, err
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
	rollbackMigration := func(dsCreated bool, reason error) (migrateResult, int, error) {
		// Use a fresh context so rollback still destroys any partial dataset
		// and restarts the app even if the migration context timed out or was
		// cancelled.
		rbCtx, rbCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer rbCancel()
		var opLog strings.Builder
		fmt.Fprintf(&opLog, "Moving %s (%s -> %s)\n", name, plan.SourceDir, plan.TargetDataset)
		fmt.Fprintf(&opLog, "Error: %v\n", reason)
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
			_ = dataset.Destroy(rbCtx, plan.TargetDataset, name)
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
			if stream, uerr := be.Up(rbCtx, st); uerr == nil {
				if out, _ := io.ReadAll(stream); len(out) > 0 {
					opLog.Write(out)
				}
				stream.Close()
			}
		}
		n := newNote(history.Snapshot, trigger)
		n.excerpt = history.Excerpt(opLog.String())
		s.writeHistory(name, n, true, "Move to dataset failed: "+history.FirstLine(reason.Error()))
		return migrateResult{}, http.StatusConflict, reason
	}

	copySrc := plan.SourceDir
	if !plan.CrossLocation {
		if err := os.Rename(plan.SourceDir, backupDir); err != nil {
			return rollbackMigration(false, fmt.Errorf("rename %s: %w", plan.SourceDir, err))
		}
		copySrc = backupDir
	}

	createdDS, err := dataset.Create(ctx, parentDS, name)
	if err != nil {
		return rollbackMigration(false, err)
	}

	if fi, serr := os.Stat(dstDir); serr != nil || !fi.IsDir() {
		return rollbackMigration(true, fmt.Errorf("target dataset mount %s not available", dstDir))
	}

	if hasOwner {
		if err := os.Chown(dstDir, uid, gid); err != nil {
			log.Printf("migrate %s: chown %s: %v", name, dstDir, err)
		}
		if err := os.Chmod(dstDir, mode); err != nil {
			log.Printf("migrate %s: chmod %s: %v", name, dstDir, err)
		}
	}

	report("Copying", 0, plan.SizeBytes)
	if err := copyTreePreservingWithProgress(ctx, copySrc, dstDir, func(copied int64) {
		report("Copying", copied, plan.SizeBytes)
	}); err != nil {
		return rollbackMigration(true, err)
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
			return rollbackMigration(true, fmt.Errorf("could not rewrite %s in stack configuration", plan.SourceDir))
		}
		if cChanged || eChanged {
			if _, err := s.manager.Update(name, func(cur *stack.Stack) error {
				cur.Compose = newCompose
				cur.Env = newEnv
				return nil
			}); err != nil {
				return rollbackMigration(true, fmt.Errorf("update stack config: %w", err))
			}
			configRewritten = true
		}
		if dChanged && directorPath != "" {
			if err := stack.WriteFileAtomic(directorPath, []byte(newDirector), 0o644); err != nil {
				return rollbackMigration(true, fmt.Errorf("update appjail-director.yml: %w", err))
			}
			directorRewritten = true
		}
	}

	report("Saving restore point", plan.SizeBytes, plan.SizeBytes)
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
		report("Starting", plan.SizeBytes, plan.SizeBytes)
		stream, uerr := be.Up(ctx, st)
		if uerr != nil {
			return rollbackMigration(true, fmt.Errorf("restart after migration: %w", uerr))
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

	n := newNote(history.Snapshot, trigger)
	n.detail = "Moved to dataset " + createdDS
	s.writeHistory(name, n, false, "")
	s.fleet.forget(name)

	return migrateResult{Dataset: createdDS, TargetDir: dstDir, BackupDir: backupDir}, http.StatusOK, nil
}

// handleMigrateAll starts a daemon-owned background job that moves every
// eligible plain-folder stack onto its own ZFS dataset, one app at a time.
func (s *server) handleMigrateAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if !dataset.HasZFS(ctx) {
		http.Error(w, "ZFS is not available on this host", http.StatusConflict)
		return
	}
	if !s.migrateAll.begin(0, keepOld) {
		http.Error(w, "A bulk dataset migration is already running", http.StatusConflict)
		return
	}

	jobID := s.jobs.start("migrate-all", "Moving apps to ZFS datasets", true, "Scanning eligible apps", nil)
	trigger := triggerOf(r)
	go func() {
		defer s.migrateAll.finish()
		scanCtx, scanCancel := context.WithTimeout(daemonCtx, 2*time.Minute)
		stacks, err := s.manager.List()
		type candidate struct {
			name string
			size int64
		}
		var cands []candidate
		if err == nil {
			for _, st := range stacks {
				full, gerr := s.manager.Get(st.Name)
				if gerr != nil {
					continue
				}
				plan := s.planMigrateDataset(scanCtx, full)
				if plan.Eligible {
					cands = append(cands, candidate{name: full.Name, size: plan.SizeBytes})
				}
			}
		}
		scanCancel()

		if err != nil {
			s.jobs.finish(jobID, "failed", "Could not list apps: "+history.FirstLine(err.Error()))
			return
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].name < cands[j].name })
		if len(cands) == 0 {
			s.jobs.finish(jobID, "done", "No apps need moving")
			return
		}
		s.migrateAll.setTotal(len(cands))
		items := make([]bgJobItem, len(cands))
		for i, c := range cands {
			detail := ""
			if c.size > 0 {
				detail = formatBytes(c.size)
			}
			items[i] = bgJobItem{Name: c.name, Status: "queued", Detail: detail}
		}
		s.jobs.setItems(jobID, items)

		moved, failed := 0, 0
		stoppedEarly := false
		for i, c := range cands {
			if s.jobs.isStopping(jobID) || !s.migrateAll.setCurrent(c.name) {
				stoppedEarly = true
				for _, rem := range cands[i:] {
					s.jobs.recordItem(jobID, rem.name, "skipped", "Stopped")
				}
				log.Printf("migrate-all: stopped by request after %d of %d", moved+failed, len(cands))
				break
			}
			s.jobs.setPhase(jobID, c.name, "Stopping", 0, c.size)
			mctx, mcancel := context.WithTimeout(daemonCtx, 30*time.Minute)
			res, _, merr := s.migrateStackToDataset(mctx, c.name, keepOld, trigger, func(phase string, bytesDone, bytesTotal int64) {
				s.jobs.setPhase(jobID, c.name, phase, bytesDone, bytesTotal)
			})
			mcancel()
			s.migrateAll.record(c.name, merr)
			if merr != nil {
				failed++
				log.Printf("migrate-all %s: %v", c.name, merr)
				s.jobs.recordItem(jobID, c.name, "failed", history.FirstLine(merr.Error()))
			} else {
				moved++
				detail := res.Dataset
				if c.size > 0 {
					detail = formatBytes(c.size)
				}
				s.jobs.recordItem(jobID, c.name, "done", detail)
			}
		}

		switch {
		case stoppedEarly:
			s.jobs.finish(jobID, "stopped", fmt.Sprintf("Stopped after moving %d of %d apps", moved, len(cands)))
		case failed > 0:
			s.jobs.finish(jobID, "failed", fmt.Sprintf("Moved %d of %d apps; %d failed", moved, len(cands), failed))
		default:
			s.jobs.finish(jobID, "done", fmt.Sprintf("Moved %d of %d apps to ZFS datasets", moved, len(cands)))
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(s.migrateAll.snapshot())
}

// handleMigrateAllCancel asks a running migrate-all batch to stop cleanly
// after the currently moving stack finishes.
func (s *server) handleMigrateAllCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.migrateAll.cancel()
	w.WriteHeader(http.StatusNoContent)
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
		HasZFS:     hasZFS,
		Locations:  []storageLocationInfo{},
		Stacks:     []storageStackInfo{},
		MigrateAll: s.migrateAll.snapshot(),
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
