package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/dataset"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/history"
	"github.com/daemonless/fjord/pkg/stack"
)

const keptCopyTTL = 30 * 24 * time.Hour

// setSnapshotKeepFn is the dataset hook used by PATCH /api/stacks/<name>/snapshots; tests replace it.
var setSnapshotKeepFn = dataset.SetSnapshotKeep

type restorePointView struct {
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Note        string    `json:"note,omitempty"`
	HostPath    string    `json:"hostPath,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UsedBytes   int64     `json:"usedBytes"`
	KeepForever bool      `json:"keepForever,omitempty"`
	Clones      []string  `json:"clones,omitempty"`
}

type restorePointsResponse struct {
	Dataset            string             `json:"dataset,omitempty"`
	HasZFS             bool               `json:"hasZfs"`
	Pinned             bool               `json:"pinned"`
	PinnedServices     []string           `json:"pinnedServices,omitempty"`
	CurrentVersion     string             `json:"currentVersion,omitempty"`
	Policy             string             `json:"policy"`
	RetentionDays      int                `json:"retentionDays"`
	DailyTime          string             `json:"dailyTime"`
	RestoredFolders    []string           `json:"restoredFolders"`
	NotRestoredFolders []string           `json:"notRestoredFolders"`
	NotCovered         []string           `json:"notCovered"`
	Snapshots          []restorePointView `json:"snapshots"`
	KeptCopies         []dataset.KeptCopy `json:"keptCopies"`
}

// isHostSystemMount reports whether a bind mount source is a host system file,
// device, or socket (like /etc/localtime or /var/run/docker.sock) rather than
// user data that belongs in a "Not included" restore list.
func isHostSystemMount(p string) bool {
	switch p {
	case "/etc/localtime", "/etc/timezone", "/etc/resolv.conf", "/etc/hosts", "/dev", "/proc", "/sys", "/run", "/var/run":
		return true
	}
	for _, prefix := range []string{"/dev/", "/proc/", "/sys/", "/run/", "/var/run/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return strings.HasSuffix(p, ".sock")
}

// notCoveredPaths returns sorted unique user-data paths and named volumes on st
// that live outside dsMount (and therefore are not rolled back when restoring
// the stack's dataset). Host system files/sockets like /etc/localtime are
// omitted.
func notCoveredPaths(st *stack.Stack, dsMount string) []string {
	out := []string{}
	if st == nil {
		return out
	}
	if dsMount != "" {
		dsMount = filepath.Clean(dsMount)
	}
	seen := map[string]bool{}
	for _, m := range stackMounts(st) {
		if m.Kind == "volume" && m.Source != "" {
			if !seen[m.Source] {
				seen[m.Source] = true
				out = append(out, m.Source)
			}
			continue
		}
		if m.Kind != "bind" || !filepath.IsAbs(m.Source) {
			continue
		}
		src := filepath.Clean(m.Source)
		if isHostSystemMount(src) {
			continue
		}
		if dsMount != "" && (src == dsMount || strings.HasPrefix(src, dsMount+string(filepath.Separator))) {
			continue
		}
		if !seen[src] {
			seen[src] = true
			out = append(out, src)
		}
	}
	sort.Strings(out)
	return out
}

func (s *server) stackRestorePoints(w http.ResponseWriter, r *http.Request, name string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	_, retDays, dailyTime := restorePointSettingsOf(loadSettings(s.fjordRoot))
	resp := restorePointsResponse{
		HasZFS:             dataset.HasZFS(ctx),
		Policy:             effectiveRestorePointPolicy(st.State),
		RetentionDays:      retDays,
		DailyTime:          dailyTime,
		RestoredFolders:    []string{},
		NotRestoredFolders: []string{},
		NotCovered:         []string{},
		Snapshots:          []restorePointView{},
		KeptCopies:         []dataset.KeptCopy{},
	}

	if st.State != nil {
		resp.Dataset = st.State.Dataset
		resp.CurrentVersion = st.State.AppVersion
	}
	if resp.Dataset == "" {
		if ds, ok := dataset.LiveByTag(ctx, st.Name); ok {
			resp.Dataset = ds
		}
	}

	if list, err := composepkg.ServiceImageList(st.Compose); err == nil {
		var vers []string
		for _, si := range list {
			if strings.Contains(si.Image, "@") {
				resp.Pinned = true
				resp.PinnedServices = append(resp.PinnedServices, si.Service)
			}
			ref := withoutDigest(si.Image)
			if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
				vers = append(vers, ref[colon+1:])
			} else if at := strings.LastIndex(si.Image, "@"); at >= 0 && len(si.Image) > at+15 {
				vers = append(vers, si.Image[at+1:at+16])
			}
		}
		if resp.CurrentVersion == "" && len(vers) > 0 {
			resp.CurrentVersion = strings.Join(vers, ", ")
		}
	}

	var dsMount string
	if resp.Dataset != "" {
		if mp := dataset.Mountpoint(ctx, resp.Dataset); mp != "" {
			dsMount = filepath.Clean(mp)
		}
	}
	seenRestored := map[string]bool{}
	seenOther := map[string]bool{}
	for _, m := range stackMounts(st) {
		if m.Kind == "volume" {
			label := m.Source + " (named volume)"
			if !seenOther[label] {
				seenOther[label] = true
				resp.NotRestoredFolders = append(resp.NotRestoredFolders, label)
			}
			continue
		}
		if m.Kind != "bind" || !filepath.IsAbs(m.Source) {
			continue
		}
		src := filepath.Clean(m.Source)
		if dsMount != "" && (src == dsMount || strings.HasPrefix(src, dsMount+string(filepath.Separator))) {
			if !seenRestored[src] {
				seenRestored[src] = true
				resp.RestoredFolders = append(resp.RestoredFolders, src)
			}
		} else if !seenOther[src] {
			seenOther[src] = true
			resp.NotRestoredFolders = append(resp.NotRestoredFolders, src+" (external bind)")
		}
	}
	sort.Strings(resp.RestoredFolders)
	sort.Strings(resp.NotRestoredFolders)
	resp.NotCovered = notCoveredPaths(st, dsMount)

	if resp.Dataset != "" {
		if snaps, err := dataset.ListSnapshots(ctx, resp.Dataset, false); err == nil {
			for i := len(snaps) - 1; i >= 0; i-- {
				sn := snaps[i]
				hostPath := ""
				if dsMount != "" {
					if _, shortName, ok := strings.Cut(sn.Name, "@"); ok && shortName != "" {
						hostPath = filepath.Join(dsMount, ".zfs", "snapshot", shortName)
					}
				}
				resp.Snapshots = append(resp.Snapshots, restorePointView{
					Name:        sn.Name,
					Kind:        sn.Kind,
					Note:        sn.Note,
					HostPath:    hostPath,
					CreatedAt:   sn.Created,
					UsedBytes:   sn.UsedBytes,
					KeepForever: sn.KeepForever,
					Clones:      sn.Clones,
				})
			}
		}
		if kept, err := dataset.ListKept(ctx, st.Name); err == nil {
			sort.Slice(kept, func(i, j int) bool { return kept[i].CreatedAt.After(kept[j].CreatedAt) })
			resp.KeptCopies = kept
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// stopStackForZFS stops the stack and verifies no containers remain running
// before any ZFS rollback or rename runs underneath its data directory.
func stopStackForZFS(ctx context.Context, be engine.Backend, st *stack.Stack) error {
	stream, err := be.Down(ctx, st)
	if err != nil {
		return fmt.Errorf("could not stop %s before restore: %w", st.Name, err)
	}
	io.Copy(io.Discard, stream)
	stream.Close()

	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	status, err := be.Status(checkCtx, st)
	if err != nil {
		return fmt.Errorf("could not verify %s is stopped: %w", st.Name, err)
	}
	var alive []string
	for _, c := range status.Containers {
		if c.State != "stopped" && c.State != "" {
			alive = append(alive, c.Name+" ("+c.State+")")
		}
	}
	if len(alive) > 0 {
		return fmt.Errorf("%s is still running (%s); stop it before restoring data", st.Name, strings.Join(alive, ", "))
	}
	return nil
}

// pinImagesFromJSON pins each service named in imagesJSON (or a single image
// ref on a single-service stack) in the stack's compose.
func (s *server) pinImagesFromJSON(name string, be engine.Backend, st *stack.Stack, imagesSpec string) {
	if imagesSpec == "" || !be.Capabilities().PinServices {
		return
	}
	var byService map[string]string
	if strings.HasPrefix(imagesSpec, "{") {
		_ = json.Unmarshal([]byte(imagesSpec), &byService)
	}
	if len(byService) > 0 {
		_, _ = s.manager.Update(name, func(cur *stack.Stack) error {
			for svc, img := range byService {
				if img != "" {
					_ = setServiceImage(cur, svc, img)
				}
			}
			return nil
		})
		return
	}
	if list, err := composepkg.ServiceImageList(st.Compose); err == nil && len(list) == 1 {
		_, _ = s.manager.Update(name, func(cur *stack.Stack) error {
			return setServiceImage(cur, list[0].Service, imagesSpec)
		})
	}
}

// stackRestore handles POST /api/stacks/<name>/restore.
// Body: {"mode": "app"|"both", "snapshot": "...", "keepCopy": true, "services": [...]}
func (s *server) stackRestore(w http.ResponseWriter, r *http.Request, name string) {
	var req struct {
		Mode     string   `json:"mode"`
		Snapshot string   `json:"snapshot"`
		KeepCopy *bool    `json:"keepCopy"`
		Services []string `json:"services"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Mode != "app" && req.Mode != "both" {
		http.Error(w, `mode must be "app" or "both"`, http.StatusBadRequest)
		return
	}
	keepCopy := true
	if req.KeepCopy != nil {
		keepCopy = *req.KeepCopy
	}

	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}

	if req.Mode == "app" {
		svcs := req.Services
		if len(svcs) == 0 && st.State != nil {
			for svc := range st.State.Rollback {
				svcs = append(svcs, svc)
			}
		}
		if len(svcs) == 0 {
			http.Error(w, "no earlier app version recorded for "+name, http.StatusBadRequest)
			return
		}
		body, _ := json.Marshal(map[string][]string{"services": svcs})
		r2 := r.Clone(r.Context())
		r2.Body = io.NopCloser(strings.NewReader(string(body)))
		s.stackRollback(w, r2, name)
		return
	}

	// Detach from HTTP request cancellation so closing the browser tab mid-ZFS
	// rename/clone/promote can never kill the operation or its rollback.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()

	dsName := ""
	if st.State != nil {
		dsName = st.State.Dataset
	}
	if dsName == "" {
		if ds, ok := dataset.LiveByTag(ctx, name); ok {
			dsName = ds
		}
	}
	if dsName == "" {
		http.Error(w, name+" does not have its own ZFS dataset", http.StatusBadRequest)
		return
	}
	snap := req.Snapshot
	if snap == "" && st.State != nil {
		snap = st.State.UpdateSnapshot
	}
	if snap == "" {
		http.Error(w, "snapshot is required for data restore", http.StatusBadRequest)
		return
	}

	unlock, ok := lockStackAs(name, "restore")
	if !ok {
		http.Error(w, anotherOperation(name), http.StatusConflict)
		return
	}
	defer unlock()

	// Look up the snapshot's recorded images (if any) so we pin the exact
	// version that wrote this snapshot, not blindly State.Rollback.
	var targetSnap *dataset.SnapInfo
	if snaps, serr := dataset.ListSnapshots(ctx, dsName, true); serr == nil {
		for i := range snaps {
			if snaps[i].Name == snap {
				targetSnap = &snaps[i]
				break
			}
		}
	}

	prevVersion, prevImagesJSON := currentStackVersionImage(ctx, s, st)
	lp, _ := dataset.LocalProperties(ctx, dsName)

	be := s.backendFor(st)
	var opLog strings.Builder
	fmt.Fprintf(&opLog, "Restoring %s to %s\n", name, snap)
	if err := stopStackForZFS(ctx, be, st); err != nil {
		fmt.Fprintf(&opLog, "Stop failed: %v\n", err)
		n := newNote(history.Rollback, triggerOf(r))
		n.excerpt = history.Excerpt(opLog.String())
		s.writeHistory(name, n, true, err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	wasRunning := st.State != nil && st.State.DesiredState == "running"
	now := time.Now().UTC()
	var keptName string
	if keepCopy {
		expectedKept := dsName + ".kept-" + now.Format("2006-01-02-150405")
		_ = s.manager.UpdateState(name, func(state *stack.State) *stack.State {
			if state == nil {
				return nil
			}
			state.Dataset = dsName
			state.PendingDatasetOp = &stack.PendingDatasetOp{
				Kind:        "restore_keep",
				LiveDataset: dsName,
				KeptDataset: expectedKept,
				Snapshot:    snap,
				LocalMP:     lp.LocalMP,
				StartedAt:   now,
			}
			return state
		})
		keptName, err = dataset.RestoreWithKeep(ctx, dsName, snap, name, prevVersion, prevImagesJSON, now, keptCopyTTL)
		_ = s.manager.UpdateState(name, func(state *stack.State) *stack.State {
			if state != nil {
				state.PendingDatasetOp = nil
			}
			return state
		})
	} else {
		err = dataset.Rollback(ctx, dsName, snap)
	}
	if err != nil {
		fmt.Fprintf(&opLog, "Restore failed: %v\n", err)
		if wasRunning {
			if stream, uerr := be.Up(ctx, st); uerr == nil {
				if out, _ := io.ReadAll(stream); len(out) > 0 {
					opLog.Write(out)
				}
				stream.Close()
			}
		}
		n := newNote(history.Rollback, triggerOf(r))
		n.excerpt = history.Excerpt(opLog.String())
		s.writeHistory(name, n, true, err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// Pin the exact image(s) that wrote this snapshot. Only fall back to
	// State.Rollback for legacy pre-update snapshots that predated fjord:images
	// (never for manual snapshots, which were saved under the current version).
	if targetSnap != nil && targetSnap.Images != "" {
		s.pinImagesFromJSON(name, be, st, targetSnap.Images)
	} else if (targetSnap == nil || targetSnap.Kind == "update") && st.State != nil && len(st.State.Rollback) > 0 && be.Capabilities().PinServices {
		_, _ = s.manager.Update(name, func(cur *stack.Stack) error {
			for svc, rb := range st.State.Rollback {
				if rb.Ref != "" && rb.Digest != "" {
					_ = setServiceImage(cur, svc, withoutDigest(rb.Ref)+"@"+rb.Digest)
				}
			}
			return nil
		})
	}

	if fresh, gerr := s.manager.Get(name); gerr == nil {
		st = fresh
	}
	if stream, uerr := be.Up(ctx, st); uerr == nil {
		io.Copy(io.Discard, stream)
		stream.Close()
	}

	msg := "Data restored"
	if targetSnap != nil && !targetSnap.Created.IsZero() {
		msg = "Data restored from " + targetSnap.Created.Local().Format("2006-01-02 15:04")
	}
	n := newNote(history.Rollback, triggerOf(r))
	n.detail = msg
	s.writeHistory(name, n, false, "")
	s.fleet.forget(name)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":          true,
		"keptDataset": keptName,
	})
}

// stackKeptRestore handles POST /api/stacks/<name>/kept/restore.
func (s *server) stackKeptRestore(w http.ResponseWriter, r *http.Request, name string) {
	var req struct {
		Dataset     string `json:"dataset"`
		KeepCurrent *bool  `json:"keepCurrent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Dataset == "" {
		http.Error(w, `send {"dataset": "..."}`, http.StatusBadRequest)
		return
	}
	keepCurrent := true
	if req.KeepCurrent != nil {
		keepCurrent = *req.KeepCurrent
	}

	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()

	dsName := ""
	if st.State != nil {
		dsName = st.State.Dataset
	}
	if dsName == "" {
		if ds, ok := dataset.LiveByTag(ctx, name); ok {
			dsName = ds
		}
	}
	if dsName == "" {
		http.Error(w, "Stack dataset not found", http.StatusNotFound)
		return
	}

	unlock, ok := lockStackAs(name, "restore")
	if !ok {
		http.Error(w, anotherOperation(name), http.StatusConflict)
		return
	}
	defer unlock()

	var target *dataset.KeptCopy
	if keptList, err := dataset.ListKept(ctx, name); err == nil {
		for i := range keptList {
			if keptList[i].Dataset == req.Dataset {
				target = &keptList[i]
				break
			}
		}
	}
	if target == nil {
		http.Error(w, "kept copy not found", http.StatusNotFound)
		return
	}

	prevVersion, prevImagesJSON := currentStackVersionImage(ctx, s, st)
	lp, _ := dataset.LocalProperties(ctx, dsName)

	be := s.backendFor(st)
	var opLog strings.Builder
	fmt.Fprintf(&opLog, "Restoring kept copy %s for %s\n", target.Dataset, name)
	if err := stopStackForZFS(ctx, be, st); err != nil {
		fmt.Fprintf(&opLog, "Stop failed: %v\n", err)
		n := newNote(history.Rollback, triggerOf(r))
		n.excerpt = history.Excerpt(opLog.String())
		s.writeHistory(name, n, true, err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	wasRunning := st.State != nil && st.State.DesiredState == "running"
	now := time.Now().UTC()
	kind := "restore_kept_keep"
	newKeptExpected := dsName + ".kept-" + now.Format("2006-01-02-150405")
	dropName := ""
	if !keepCurrent {
		kind = "restore_kept_drop"
		newKeptExpected = ""
		dropName = fmt.Sprintf("%s.deleting-%d", dsName, now.Unix())
	}
	_ = s.manager.UpdateState(name, func(state *stack.State) *stack.State {
		if state != nil {
			state.Dataset = dsName
			state.PendingDatasetOp = &stack.PendingDatasetOp{
				Kind:           kind,
				LiveDataset:    dsName,
				KeptDataset:    target.Dataset,
				NewKeptDataset: newKeptExpected,
				DropDataset:    dropName,
				LocalMP:        lp.LocalMP,
				StartedAt:      now,
			}
		}
		return state
	})

	newKept, err := dataset.RestoreKept(ctx, dsName, target.Dataset, name, prevVersion, prevImagesJSON, keepCurrent, now, keptCopyTTL)
	_ = s.manager.UpdateState(name, func(state *stack.State) *stack.State {
		if state != nil {
			state.PendingDatasetOp = nil
		}
		return state
	})
	if err != nil {
		fmt.Fprintf(&opLog, "Restore failed: %v\n", err)
		if wasRunning {
			if stream, uerr := be.Up(ctx, st); uerr == nil {
				if out, _ := io.ReadAll(stream); len(out) > 0 {
					opLog.Write(out)
				}
				stream.Close()
			}
		}
		n := newNote(history.Rollback, triggerOf(r))
		n.excerpt = history.Excerpt(opLog.String())
		s.writeHistory(name, n, true, err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// Keep the stack pinned to the kept copy's saved image(s) (never silently
	// resume automatic updates).
	if target.Image != "" {
		s.pinImagesFromJSON(name, be, st, target.Image)
	}

	if fresh, gerr := s.manager.Get(name); gerr == nil {
		st = fresh
	}
	if stream, uerr := be.Up(ctx, st); uerr == nil {
		io.Copy(io.Discard, stream)
		stream.Close()
	}

	n := newNote(history.Rollback, triggerOf(r))
	n.detail = "Used saved copy from " + target.CreatedAt.Format("2006-01-02 15:04")
	s.writeHistory(name, n, false, "")
	s.fleet.forget(name)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":          true,
		"keptDataset": newKept,
	})
}

// stackSnapshots handles POST (create manual restore point) and DELETE
// (remove manual restore point) on /api/stacks/<name>/snapshots.
func (s *server) stackSnapshots(w http.ResponseWriter, r *http.Request, name string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()

	dsName := ""
	if st.State != nil {
		dsName = st.State.Dataset
	}
	if dsName == "" {
		if ds, ok := dataset.LiveByTag(ctx, name); ok {
			dsName = ds
		}
	}
	if dsName == "" {
		http.Error(w, name+" does not have its own ZFS dataset", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPost:
		var req struct {
			Note string `json:"note"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		_, imagesJSON := currentStackVersionImage(ctx, s, st)
		snap, err := dataset.SnapshotManual(ctx, dsName, name, req.Note, time.Now().UTC(), imagesJSON)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		msg := strings.TrimSpace(req.Note)
		n := newNote(history.Snapshot, triggerOf(r))
		n.detail = msg
		s.writeHistory(name, n, false, "")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"snapshot": snap})
	case http.MethodPatch:
		var req struct {
			Snapshot string `json:"snapshot"`
			Forever  bool   `json:"forever"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Snapshot == "" {
			http.Error(w, `send {"snapshot": "...", "forever": true|false}`, http.StatusBadRequest)
			return
		}
		if !dataset.IsFjordSnap(dsName, req.Snapshot) {
			http.Error(w, "not a fjord restore point for "+name, http.StatusBadRequest)
			return
		}
		if err := setSnapshotKeepFn(ctx, dsName, req.Snapshot, name, req.Forever); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		snap := r.URL.Query().Get("snapshot")
		if snap == "" {
			http.Error(w, "snapshot query param required", http.StatusBadRequest)
			return
		}
		if _, err := dataset.DestroySnapshot(ctx, dsName, snap, name); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

// stackKeptManage handles PATCH (Keep forever) and DELETE (Delete copy) on
// /api/stacks/<name>/kept.
func (s *server) stackKeptManage(w http.ResponseWriter, r *http.Request, name string) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()

	switch r.Method {
	case http.MethodPatch:
		var req struct {
			Dataset string `json:"dataset"`
			Forever bool   `json:"forever"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Dataset == "" {
			http.Error(w, `send {"dataset": "...", "forever": true|false}`, http.StatusBadRequest)
			return
		}
		until := time.Now().UTC().Add(keptCopyTTL)
		if err := dataset.SetKeptForever(ctx, req.Dataset, name, req.Forever, until); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		ds := r.URL.Query().Get("dataset")
		if ds == "" {
			http.Error(w, "dataset query param required", http.StatusBadRequest)
			return
		}
		unlock, ok := lockStackAs(name, "delete-copy")
		if !ok {
			http.Error(w, anotherOperation(name), http.StatusConflict)
			return
		}
		defer unlock()
		if _, err := dataset.DestroyKept(ctx, ds, name); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

// currentStackVersionImage returns a short display version and a JSON map of
// service -> ref@digest for every running service in st.
func currentStackVersionImage(ctx context.Context, s *server, st *stack.Stack) (version, imagesJSON string) {
	imgs := map[string]string{}
	var vers []string
	if ris, err := s.backendFor(st).RunningImages(ctx, st); err == nil && len(ris) > 0 {
		for _, ri := range ris {
			ref := withoutDigest(ri.Ref)
			if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
				vers = append(vers, ref[colon+1:])
			}
			if ri.Digest != "" {
				imgs[ri.Service] = ref + "@" + ri.Digest
			} else if ri.Ref != "" {
				imgs[ri.Service] = ri.Ref
			}
		}
	}
	if len(imgs) > 0 {
		if b, err := json.Marshal(imgs); err == nil {
			imagesJSON = string(b)
		}
	}
	if len(vers) > 0 {
		version = strings.Join(vers, ", ")
	} else if st.State != nil {
		version = st.State.AppVersion
	}
	return version, imagesJSON
}

func formatBytes(b int64) string {
	const mb = 1024 * 1024
	const gb = 1024 * mb
	if b >= gb {
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	}
	if b >= mb {
		return fmt.Sprintf("%d MB", b/mb)
	}
	if b >= 1024 {
		return fmt.Sprintf("%d KB", b/1024)
	}
	return fmt.Sprintf("%d B", b)
}

// runKeptCopySweep destroys expired kept-copy datasets periodically, taking
// the stack lock and using the dataset's fjord:stack tag for ownership.
func runKeptCopySweep(srv *server) {
	sweep := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		kept, err := dataset.ListKept(ctx, "")
		if err != nil {
			return
		}
		now := time.Now().UTC()
		for _, c := range kept {
			if c.Until == "" || c.Stack == "" || c.Stack == "-" {
				continue
			}
			exp, err := time.Parse(time.RFC3339, c.Until)
			if err != nil || now.Before(exp) {
				continue
			}
			unlock, ok := lockStackAs(c.Stack, "sweep")
			if !ok {
				continue // busy with restore/update; retry next tick
			}
			freed, err := dataset.DestroyKept(ctx, c.Dataset, c.Stack)
			unlock()
			if err != nil {
				log.Printf("kept-copy sweep %s: %v", c.Dataset, err)
				continue
			}
			n := newNote(history.Delete, history.ByAutomatic)
			n.detail = fmt.Sprintf("Removed a kept copy (%s freed)", formatBytes(freed))
			srv.writeHistory(c.Stack, n, false, "")
		}
		_, retDays, _ := restorePointSettingsOf(loadSettings(srv.fjordRoot))
		maxAge := time.Duration(retDays) * 24 * time.Hour
		if stacks, err := srv.manager.List(); err == nil {
			for _, st := range stacks {
				state, _ := srv.manager.LoadState(st.Name)
				if state == nil || state.Dataset == "" || state.DesiredState == "deleting" {
					continue
				}
				unlock, ok := lockStackAs(st.Name, "sweep")
				if !ok {
					continue
				}
				_, err := dataset.PruneAutomaticSnapshots(ctx, state.Dataset, maxAge, now)
				unlock()
				if err != nil {
					log.Printf("restore-point sweep %s: %v", st.Name, err)
				}
			}
		}
	}
	time.Sleep(30 * time.Second)
	sweep()
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for range t.C {
		sweep()
	}
}
