package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/dataset"
	"github.com/daemonless/fjord/pkg/stack"
)

// keptRecord is what fjord knows about app data left on disk with no stack:
// the data a delete kept, or a folder a fresh install set aside. The data
// folder alone says neither when it was last used nor by which version.
type keptRecord struct {
	Path    string `json:"path"`
	Dataset string `json:"dataset,omitempty"`
	// Name is the id of the stack the data belonged to.
	Name      string    `json:"name"`
	AppID     string    `json:"appId,omitempty"`
	Catalog   string    `json:"catalog,omitempty"`
	DeletedAt time.Time `json:"deletedAt"`
	Version   string    `json:"version,omitempty"`
	// SetAside: a plain folder an install that started fresh moved out of the
	// way. It is removed after Until. A dataset set aside is a kept copy
	// instead, and carries this in its own properties.
	SetAside bool       `json:"setAside,omitempty"`
	Until    *time.Time `json:"until,omitempty"`
}

// The dataset calls kept data makes; tests replace them. It also uses
// listSnapshotsFn, declared with the daily restore points.
var (
	listLiveDatasetsFn = dataset.ListLive
	listKeptCopiesFn   = dataset.ListKept
	destroyKeptCopyFn  = dataset.DestroyKept
	destroyDatasetFn   = dataset.Destroy
	setAsideDatasetFn  = dataset.SetAside
	datasetMountFn     = dataset.Mountpoint
	datasetAtFn        = dataset.At
	datasetOwnedFn     = dataset.Owned
)

var keptDataMu sync.Mutex

func keptDataPath(root string) string { return filepath.Join(root, "keptdata.json") }

func readKeptRecords(root string) []keptRecord {
	b, err := os.ReadFile(keptDataPath(root))
	if err != nil {
		return nil
	}
	var recs []keptRecord
	if json.Unmarshal(b, &recs) != nil {
		return nil
	}
	return recs
}

func loadKeptRecords(root string) []keptRecord {
	keptDataMu.Lock()
	defer keptDataMu.Unlock()
	return readKeptRecords(root)
}

// updateKeptRecords rewrites the records under the lock.
func updateKeptRecords(root string, fn func([]keptRecord) []keptRecord) error {
	keptDataMu.Lock()
	defer keptDataMu.Unlock()
	recs := fn(readKeptRecords(root))
	if recs == nil {
		recs = []keptRecord{}
	}
	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	return stack.WriteFileAtomic(keptDataPath(root), b, 0o600)
}

// recordKept keeps rec, in place of any record for the same path.
func recordKept(root string, rec keptRecord) {
	rec.Path = filepath.Clean(rec.Path)
	err := updateKeptRecords(root, func(recs []keptRecord) []keptRecord {
		out := recs[:0]
		for _, r := range recs {
			if r.Path != rec.Path {
				out = append(out, r)
			}
		}
		return append(out, rec)
	})
	if err != nil {
		log.Printf("kept data: record %s: %v", rec.Path, err)
	}
}

// forgetKept drops the record for dir: its data is in use again, or gone.
func forgetKept(root, dir string) {
	dir = filepath.Clean(dir)
	found := false
	for _, r := range loadKeptRecords(root) {
		found = found || r.Path == dir
	}
	if !found {
		return
	}
	err := updateKeptRecords(root, func(recs []keptRecord) []keptRecord {
		out := recs[:0]
		for _, r := range recs {
			if r.Path != dir {
				out = append(out, r)
			}
		}
		return out
	})
	if err != nil {
		log.Printf("kept data: forget %s: %v", dir, err)
	}
}

func keptRecordAt(root, dir string) (keptRecord, bool) {
	dir = filepath.Clean(dir)
	for _, r := range loadKeptRecords(root) {
		if r.Path == dir {
			return r, true
		}
	}
	return keptRecord{}, false
}

// keptDataOf is the record of each data folder deleting st without its data
// leaves behind. Worked out before the stack goes: the folders are its
// delete plan's, the version its image's.
func (s *server) keptDataOf(ctx context.Context, st *stack.Stack) []keptRecord {
	dirs := s.planDelete(ctx, st).AppData
	if len(dirs) == 0 {
		return nil
	}
	rec := keptRecord{Name: st.Name, DeletedAt: time.Now().UTC(), Version: s.appVersionOf(ctx, st)}
	if st.State != nil {
		rec.AppID, rec.Catalog = st.State.Origin.AppID, st.State.Origin.Catalog
	}
	out := make([]keptRecord, 0, len(dirs))
	for _, d := range dirs {
		r := rec
		r.Path, r.Dataset = d.Path, d.Dataset
		out = append(out, r)
	}
	return out
}

// appVersionOf is the version of the app st runs, from its image's version
// label: the app's own service when one is named after it, else the first
// that says. "" when no image says, or the engine cannot read labels.
func (s *server) appVersionOf(ctx context.Context, st *stack.Stack) string {
	lv, ok := s.backendFor(st).(localVersions)
	if !ok {
		return ""
	}
	appID := ""
	if st.State != nil {
		appID = st.State.Origin.AppID
	}
	first := ""
	for _, sv := range s.updateServices(ctx, st) {
		ref := withoutDigest(sv.Image)
		if ref == "" {
			continue
		}
		repo := ref
		if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
			repo = ref[:colon]
		}
		if sv.Running != "" {
			ref = repo + "@" + sv.Running
		}
		v, err := lv.ImageVersion(ctx, ref)
		if err != nil || v == "" {
			continue
		}
		if appID != "" && strings.Contains(path.Base(repo), appID) {
			return v
		}
		if first == "" {
			first = v
		}
	}
	return first
}

// keptDatasetAt is the dataset an earlier install under this name left
// mounted at dir, when fjord made it for that name and nothing in it is
// someone else's. "" for a plain folder, or a dataset that is not the app's.
func keptDatasetAt(ctx context.Context, dir, slug string) string {
	ds, ok := datasetAtFn(ctx, dir)
	if !ok || datasetOwnedFn(ctx, ds, slug) != nil {
		return ""
	}
	log.Printf("install %s: using its dataset %s from an earlier install", slug, ds)
	return ds
}

// rememberKeptData records what a delete left on disk.
func (s *server) rememberKeptData(recs []keptRecord) {
	for _, r := range recs {
		if isRealDir(r.Path) {
			recordKept(s.fjordRoot, r)
		}
	}
}

// errKeptInUse: the folder an install would start fresh in is not left-over
// data, so it is not fjord's to move.
var errKeptInUse = errors.New("a stack or a container still uses it")

// setAsideKeptData moves the data an earlier install left in dataDir out of
// the way, for an install that starts fresh. It is never deleted here: a
// dataset becomes a kept copy of the stack, a plain folder is renamed and
// recorded, and both are removed when the kept-copy time is up. Returns a
// line for the install's output, "" when there was nothing to set aside.
func (s *server) setAsideKeptData(ctx context.Context, dataDir, slug string) (string, error) {
	dataDir = filepath.Clean(dataDir)
	if !isRealDir(dataDir) {
		return "", nil
	}
	found, err := s.findLeftovers(ctx)
	if err != nil {
		return "", fmt.Errorf("could not work out whether %s is in use: %w", dataDir, err)
	}
	var left *leftoverDir
	for i := range found.Folders {
		if found.Folders[i].Path == dataDir && !found.Folders[i].SetAside {
			left = &found.Folders[i]
		}
	}
	if left == nil {
		return "", fmt.Errorf("%s: %w", dataDir, errKeptInUse)
	}
	now := time.Now().UTC()
	until := now.Add(keptCopyTTL)
	rec, _ := keptRecordAt(s.fjordRoot, dataDir)
	aside := ""
	if left.Dataset != "" {
		kept, err := setAsideDatasetFn(ctx, left.Dataset, left.tag, dataset.KindFreshInstall, rec.Version, now, keptCopyTTL)
		if err != nil {
			return "", err
		}
		aside = datasetMountFn(ctx, kept)
		log.Printf("install %s: set the earlier data aside as kept copy %s", slug, kept)
	} else {
		aside = dataDir + ".kept-" + now.Format("2006-01-02-150405")
		if err := os.Rename(dataDir, aside); err != nil {
			return "", fmt.Errorf("could not set %s aside: %w", dataDir, err)
		}
		if rec.Name == "" {
			rec.Name, rec.DeletedAt = slug, now
		}
		rec.Path, rec.Dataset, rec.SetAside, rec.Until = aside, "", true, &until
		recordKept(s.fjordRoot, rec)
		log.Printf("install %s: set the earlier data aside in %s", slug, aside)
	}
	forgetKept(s.fjordRoot, dataDir)
	// The secrets it was set up with stay with it, not with the new install.
	if aside != "" {
		if err := os.Rename(secretsPath(s.fjordRoot, dataDir), secretsPath(s.fjordRoot, aside)); err != nil && !os.IsNotExist(err) {
			log.Printf("install %s: move the earlier data's secrets: %v", slug, err)
		}
	} else {
		os.Remove(secretsPath(s.fjordRoot, dataDir))
	}
	where := aside
	if where == "" {
		where = "a kept copy"
	}
	return fmt.Sprintf("[fjord] Starting fresh: the data from the earlier install was set aside in %s and is removed on %s.\n",
		where, until.Format("2006-01-02")), nil
}

// sweepSetAside removes the plain folders set aside by a fresh install whose
// time is up. Only a folder fjord recorded, still named as one it set aside.
func (s *server) sweepSetAside(now time.Time) {
	for _, r := range loadKeptRecords(s.fjordRoot) {
		if !r.SetAside || r.Until == nil || now.Before(*r.Until) {
			continue
		}
		if !dataset.IsInternalDir(filepath.Base(r.Path)) || !isRealDir(r.Path) {
			forgetKept(s.fjordRoot, r.Path)
			continue
		}
		if err := os.RemoveAll(r.Path); err != nil {
			log.Printf("kept data: remove %s: %v", r.Path, err)
			continue
		}
		os.Remove(secretsPath(s.fjordRoot, r.Path))
		forgetKept(s.fjordRoot, r.Path)
		log.Printf("kept data: removed %s, set aside by a fresh install of %s", r.Path, r.Name)
	}
}

// runKeptDataSweep removes expired set-aside folders, soon after start and
// then every six hours, as the kept-copy sweep does for datasets.
func runKeptDataSweep(srv *server) {
	time.Sleep(45 * time.Second)
	srv.sweepSetAside(time.Now())
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for now := range t.C {
		srv.sweepSetAside(now)
	}
}
