package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/dataset"
	"github.com/daemonless/fjord/pkg/engine"
)

// leftovers is app data nothing uses any more: what deleting a stack left
// behind before delete could take its data along (and still does, unticked).
type leftovers struct {
	Folders []leftoverDir `json:"folders"`
	// Unchecked names engines that cannot list their containers' mounts: a
	// folder here may still be used by one of their containers.
	Unchecked []string `json:"unchecked,omitempty"`
}

// leftoverDir is one left-over data folder, with what fjord recorded about it
// when its stack was deleted.
type leftoverDir struct {
	dataDir
	// Name is the stack the data belonged to: recorded at delete, the
	// dataset's tag, or the folder's name.
	Name    string `json:"name,omitempty"`
	AppID   string `json:"appId,omitempty"`
	Catalog string `json:"catalog,omitempty"`
	// DeletedAt and Version are known only for a delete fjord recorded.
	DeletedAt *time.Time `json:"deletedAt,omitempty"`
	Version   string     `json:"version,omitempty"`
	// Installable: the catalog has the app, so it can be installed again on
	// this data.
	Installable bool `json:"installable,omitempty"`
	// RestorePoints and KeptCopies go with a dataset when it is removed.
	RestorePoints int `json:"restorePoints,omitempty"`
	KeptCopies    int `json:"keptCopies,omitempty"`
	// SetAside: moved out of the way by an install that started fresh;
	// removed by itself after Until.
	SetAside bool       `json:"setAside,omitempty"`
	Until    *time.Time `json:"until,omitempty"`
	// tag is the stack a dataset is tagged for: whose it is to destroy.
	tag string
}

// findLeftovers lists folders in the app-data locations that no stack is
// named after, no stack binds, and no container on the host mounts -- fjord's
// or not. The last matters: on jupiter /containers also holds the data of
// containers Ansible runs, which no fjord stack knows about.
func (s *server) findLeftovers(ctx context.Context) (leftovers, error) {
	out := leftovers{Folders: []leftoverDir{}}
	stacks := map[string]bool{}
	var used []string
	list, err := s.manager.List()
	if err != nil {
		return out, err
	}
	for _, st := range list {
		stacks[st.Name] = true
		full, err := s.manager.Get(st.Name)
		if err != nil {
			return out, err // a stack we cannot read might use anything
		}
		for _, m := range stackMounts(full) {
			if m.Kind == "bind" && filepath.IsAbs(m.Source) {
				used = append(used, resolved(m.Source))
			}
		}
	}
	s.mu.RLock()
	backends := make(map[string]engine.Backend, len(s.backends))
	for k, v := range s.backends {
		backends[k] = v
	}
	s.mu.RUnlock()
	for name, be := range backends {
		ml, ok := be.(engine.MountLister)
		if !ok {
			out.Unchecked = append(out.Unchecked, name)
			continue
		}
		paths, err := ml.HostMounts(ctx)
		if err != nil {
			return out, err // unsure means nothing is offered
		}
		for _, p := range paths {
			used = append(used, resolved(p))
		}
	}
	sort.Strings(out.Unchecked)
	// The datasets fjord made, by where they are mounted: a deleted stack's
	// is removed as a dataset, with its restore points, not folder by folder.
	live, _ := listLiveDatasetsFn(ctx)
	mounted := map[string]dataset.LiveDataset{}
	for _, d := range live {
		if d.Mountpoint != "" {
			mounted[filepath.Clean(d.Mountpoint)] = d
		}
	}
	listed := map[string]bool{}
	for _, loc := range s.dataLocations() {
		entries, _ := os.ReadDir(loc)
		for _, e := range entries {
			dir := filepath.Join(loc, e.Name())
			// Compared with links followed: a mount named through a symlink
			// is this folder, in use.
			real := resolved(dir)
			if strings.HasPrefix(e.Name(), ".") || dataset.IsInternalDir(e.Name()) ||
				stacks[e.Name()] || !isRealDir(dir) ||
				anyInside(used, real) || containsAny(used, real) {
				continue
			}
			f := leftoverDir{Name: e.Name()}
			if d, ok := mounted[dir]; ok && d.Stack == e.Name() {
				f.dataDir, f.tag = dataDir{Path: dir, Dataset: d.Name, Bytes: d.UsedBytes}, d.Stack
			} else {
				n, more := dirSize(dir, time.Second)
				f.dataDir = dataDir{Path: dir, Bytes: n, More: more}
			}
			listed[dir] = true
			out.Folders = append(out.Folders, f)
		}
	}
	// A deleted stack's dataset mounted somewhere else: a location since
	// taken out of Settings, or a mountpoint set by hand.
	for _, d := range live {
		mp := filepath.Clean(d.Mountpoint)
		if d.Mountpoint == "" || listed[mp] || stacks[d.Stack] || !isRealDir(mp) {
			continue
		}
		if real := resolved(mp); anyInside(used, real) || containsAny(used, real) {
			continue
		}
		listed[mp] = true
		out.Folders = append(out.Folders, leftoverDir{
			dataDir: dataDir{Path: mp, Dataset: d.Name, Bytes: d.UsedBytes}, Name: d.Stack, tag: d.Stack,
		})
	}
	s.describeLeftovers(ctx, &out, listed)
	sort.Slice(out.Folders, func(i, j int) bool { return out.Folders[i].Path < out.Folders[j].Path })
	return out, nil
}

// describeLeftovers adds what fjord recorded when each folder's stack was
// deleted, whether the catalog can install the app again, what a dataset
// takes with it, and the folders a fresh install set aside. Records of
// folders that are gone are dropped.
func (s *server) describeLeftovers(ctx context.Context, out *leftovers, listed map[string]bool) {
	recs := map[string]keptRecord{}
	var gone []string
	for _, r := range loadKeptRecords(s.fjordRoot) {
		switch {
		case !isRealDir(r.Path):
			gone = append(gone, r.Path)
		case r.SetAside:
			if listed[r.Path] {
				continue
			}
			n, more := dirSize(r.Path, time.Second)
			at := r.DeletedAt
			out.Folders = append(out.Folders, leftoverDir{
				dataDir: dataDir{Path: r.Path, Bytes: n, More: more},
				Name:    r.Name, AppID: r.AppID, Catalog: r.Catalog, DeletedAt: &at, Version: r.Version,
				SetAside: true, Until: r.Until,
			})
		default:
			recs[r.Path] = r
		}
	}
	for _, p := range gone {
		forgetKept(s.fjordRoot, p)
	}
	apps := s.catalogApps()
	for i := range out.Folders {
		f := &out.Folders[i]
		if f.SetAside {
			continue
		}
		if r, ok := recs[f.Path]; ok {
			at := r.DeletedAt
			f.Name, f.AppID, f.Catalog, f.DeletedAt, f.Version = r.Name, r.AppID, r.Catalog, &at, r.Version
		} else if apps[f.Name] {
			// Not recorded (deleted before fjord kept records): the folder is
			// named as an install of that app would name it.
			f.AppID = f.Name
		}
		f.Installable = f.AppID != "" && apps[f.AppID]
		if f.Dataset != "" {
			if snaps, err := listSnapshotsFn(ctx, f.Dataset, false); err == nil {
				f.RestorePoints = len(snaps)
			}
			if kept, err := listKeptCopiesFn(ctx, f.tag); err == nil {
				f.KeptCopies = len(kept)
			}
		}
	}
}

// catalogApps is the ids of the apps the catalogs offer this host.
func (s *server) catalogApps() map[string]bool {
	out := map[string]bool{}
	if s.cat == nil {
		return out
	}
	raw, err := s.cat.Merged()
	if err != nil {
		return out
	}
	var doc struct {
		Apps []struct {
			ID string `json:"id"`
		} `json:"apps"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return out
	}
	for _, a := range doc.Apps {
		out[a.ID] = true
	}
	return out
}

// removeLeftover removes one left-over folder for good: a dataset with its
// restore points and kept copies, a plain folder with what is in it.
func (s *server) removeLeftover(ctx context.Context, f leftoverDir) error {
	if f.Dataset != "" {
		tag := f.tag
		// Kept copies first: they are clones of its snapshots, and it cannot
		// be destroyed under them.
		if kept, err := listKeptCopiesFn(ctx, tag); err == nil {
			for _, kc := range kept {
				if _, err := destroyKeptCopyFn(ctx, kc.Dataset, tag); err != nil {
					return fmt.Errorf("kept copy %s: %w", kc.Dataset, err)
				}
			}
		}
		if err := destroyDatasetFn(ctx, f.Dataset, tag); err != nil {
			return err
		}
	} else if err := os.RemoveAll(f.Path); err != nil {
		return err
	}
	os.Remove(secretsPath(s.fjordRoot, f.Path))
	forgetKept(s.fjordRoot, f.Path)
	return nil
}

// handleLeftovers: GET lists left-over app data; POST {"path": ...} removes
// one folder, only if it is on the list worked out right now.
func (s *server) handleLeftovers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	switch r.Method {
	case http.MethodGet:
		found, err := s.findLeftovers(ctx)
		if err != nil {
			http.Error(w, "could not work out what is in use: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(found)
	case http.MethodPost:
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			http.Error(w, "path required", 400)
			return
		}
		found, err := s.findLeftovers(ctx)
		if err != nil {
			http.Error(w, "not removed: could not work out what is in use: "+err.Error(), 500)
			return
		}
		for _, f := range found.Folders {
			if f.Path == filepath.Clean(req.Path) {
				if err := s.removeLeftover(ctx, f); err != nil {
					http.Error(w, "could not remove "+f.Path+": "+err.Error(), 500)
					return
				}
				log.Printf("removed left-over app data %s", f.Path)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		http.Error(w, req.Path+" is not left-over app data (a stack or container uses it, or it is not in an app-data location)", 409)
	default:
		http.Error(w, "method not allowed", 405)
	}
}
