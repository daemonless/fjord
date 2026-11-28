package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/dataset"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/history"
)

// No test in this package reads the host's own datasets: the left-over list
// would show whatever the machine running the tests has.
func init() {
	listLiveDatasetsFn = func(context.Context) ([]dataset.LiveDataset, error) { return nil, nil }
}

// versionedBackend is the mock engine with images that say their version.
type versionedBackend struct {
	*cliMockBackend
	versions map[string]string // image ref -> version label
}

func (v versionedBackend) ImageVersion(_ context.Context, ref string) (string, error) {
	return v.versions[ref], nil
}

func leftoverAt(t *testing.T, s *server, dir string) (leftoverDir, bool) {
	t.Helper()
	found, err := s.findLeftovers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found.Folders {
		if f.Path == dir {
			return f, true
		}
	}
	return leftoverDir{}, false
}

// A delete that keeps the data records where it is, when, and which version
// last used it; a delete that takes the data records nothing.
func TestDeleteRecordsKeptData(t *testing.T) {
	s, root := deleteFixture(t)
	s.backends["podman"] = versionedBackend{&cliMockBackend{stopped: true}, map[string]string{"x": "2.1.6"}}
	writeFile(t, filepath.Join(root, "stacks/notes/state.json"), `{"engine":"podman","origin":{"type":"catalog","app_id":"notes","catalog":"main"}}`)
	notes := filepath.Join(root, "apps/notes")

	before := time.Now().Add(-time.Second)
	rec := httptest.NewRecorder()
	s.stackDelete(rec, "notes", false, history.ByYou)
	if rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	r, ok := keptRecordAt(root, notes)
	if !ok {
		t.Fatalf("no record for %s: %+v", notes, loadKeptRecords(root))
	}
	if r.Name != "notes" || r.AppID != "notes" || r.Catalog != "main" || r.Version != "2.1.6" || r.DeletedAt.Before(before) || r.SetAside {
		t.Errorf("record: %+v", r)
	}

	s, root = deleteFixture(t)
	rec = httptest.NewRecorder()
	s.stackDelete(rec, "notes", true, history.ByYou)
	if rec.Code != 200 {
		t.Fatalf("delete with data: %d %s", rec.Code, rec.Body)
	}
	if recs := loadKeptRecords(root); len(recs) != 0 {
		t.Errorf("delete with data recorded kept data: %+v", recs)
	}
}

// The left-over list says when a recorded folder's app was deleted and its
// last version; a folder nobody recorded is listed as before.
func TestLeftoversShowTheRecord(t *testing.T) {
	s, root := leftoverFixture(t)
	old := filepath.Join(root, "apps/oldapp")
	at := time.Date(2026, 11, 20, 8, 30, 0, 0, time.UTC)
	recordKept(root, keptRecord{Path: old, Name: "oldapp", AppID: "oldapp", DeletedAt: at, Version: "1.4.2"})
	// A record whose folder is gone is dropped.
	recordKept(root, keptRecord{Path: filepath.Join(root, "apps/gone"), Name: "gone", DeletedAt: at})

	f, ok := leftoverAt(t, s, old)
	if !ok {
		t.Fatal("oldapp not listed")
	}
	if f.Name != "oldapp" || f.Version != "1.4.2" || f.DeletedAt == nil || !f.DeletedAt.Equal(at) || f.Bytes != 500 {
		t.Errorf("oldapp: %+v", f)
	}
	if f.Installable {
		t.Error("installable with no catalog")
	}
	if recs := loadKeptRecords(root); len(recs) != 1 || recs[0].Path != old {
		t.Errorf("records after listing: %+v", recs)
	}

	// The wire form the page reads.
	b, _ := json.Marshal(f)
	for _, want := range []string{`"path":"` + old + `"`, `"bytes":500`, `"deletedAt":"2026-11-20T08:30:00Z"`, `"version":"1.4.2"`, `"name":"oldapp"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json lacks %s: %s", want, b)
		}
	}
}

// Removing a left-over folder drops its record and the secrets kept for it.
func TestLeftoverRemoveForgetsTheRecord(t *testing.T) {
	s, root := leftoverFixture(t)
	old := filepath.Join(root, "apps/oldapp")
	recordKept(root, keptRecord{Path: old, Name: "oldapp", DeletedAt: time.Now()})
	writeFile(t, secretsPath(root, old), "P=x\n")

	rec := httptest.NewRecorder()
	s.handleLeftovers(rec, httptest.NewRequest("POST", "/api/maintenance/leftovers", strings.NewReader(`{"path":"`+old+`"}`)))
	if rec.Code != 204 {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	if _, ok := keptRecordAt(root, old); ok {
		t.Error("record left behind")
	}
	if _, err := os.Stat(secretsPath(root, old)); err == nil {
		t.Error("secrets left behind")
	}
}

// fakeZFS stands in for the dataset calls kept data makes.
type fakeZFS struct {
	live      []dataset.LiveDataset
	snaps     map[string]int
	kept      map[string][]dataset.KeptCopy
	destroyed []string
	setAside  []string
}

func (z *fakeZFS) install(t *testing.T) {
	oldLive, oldSnaps, oldKept := listLiveDatasetsFn, listSnapshotsFn, listKeptCopiesFn
	oldDK, oldDD, oldSA, oldMP := destroyKeptCopyFn, destroyDatasetFn, setAsideDatasetFn, datasetMountFn
	t.Cleanup(func() {
		listLiveDatasetsFn, listSnapshotsFn, listKeptCopiesFn = oldLive, oldSnaps, oldKept
		destroyKeptCopyFn, destroyDatasetFn, setAsideDatasetFn, datasetMountFn = oldDK, oldDD, oldSA, oldMP
	})
	listLiveDatasetsFn = func(context.Context) ([]dataset.LiveDataset, error) { return z.live, nil }
	listSnapshotsFn = func(_ context.Context, name string, _ bool) ([]dataset.SnapInfo, error) {
		return make([]dataset.SnapInfo, z.snaps[name]), nil
	}
	listKeptCopiesFn = func(_ context.Context, stack string) ([]dataset.KeptCopy, error) { return z.kept[stack], nil }
	destroyKeptCopyFn = func(_ context.Context, name, _ string) (int64, error) {
		z.destroyed = append(z.destroyed, "kept "+name)
		return 0, nil
	}
	destroyDatasetFn = func(_ context.Context, name, stack string) error {
		z.destroyed = append(z.destroyed, name+" for "+stack)
		return nil
	}
	setAsideDatasetFn = func(_ context.Context, live, stack, kind, version string, _ time.Time, _ time.Duration) (string, error) {
		z.setAside = append(z.setAside, strings.Join([]string{live, stack, kind, version}, " "))
		return live + ".kept-x", nil
	}
	datasetMountFn = func(_ context.Context, name string) string { return "/mnt/" + filepath.Base(name) }
}

// A deleted stack's dataset is listed as one, with what goes with it, and
// removed as a dataset -- kept copies first -- not emptied file by file. One
// mounted outside the app-data locations is listed too; a live stack's and
// one a container mounts are not.
func TestLeftoverDatasets(t *testing.T) {
	s, root := leftoverFixture(t)
	old := filepath.Join(root, "apps/oldapp")
	away := filepath.Join(root, "elsewhere-data")
	os.MkdirAll(away, 0o755)
	z := &fakeZFS{
		live: []dataset.LiveDataset{
			{Name: "tank/apps/oldapp", Stack: "oldapp", Mountpoint: old, UsedBytes: 7000},
			{Name: "tank/away/farapp", Stack: "farapp", Mountpoint: away, UsedBytes: 300},
			{Name: "tank/apps/notes", Stack: "notes", Mountpoint: filepath.Join(root, "apps/notes"), UsedBytes: 1},
			{Name: "tank/apps/ansible-svc", Stack: "ansible-svc", Mountpoint: filepath.Join(root, "apps/ansible-svc")},
			{Name: "tank/nomount", Stack: "nomount"},
		},
		snaps: map[string]int{"tank/apps/oldapp": 4},
		kept:  map[string][]dataset.KeptCopy{"oldapp": {{Dataset: "tank/apps/oldapp.kept-1"}}},
	}
	z.install(t)

	found, err := s.findLeftovers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]leftoverDir{}
	for _, f := range found.Folders {
		got[f.Path] = f
	}
	if len(got) != 2 {
		t.Fatalf("want oldapp and the dataset mounted elsewhere, got %+v", found.Folders)
	}
	if f := got[old]; f.Dataset != "tank/apps/oldapp" || f.Bytes != 7000 || f.RestorePoints != 4 || f.KeptCopies != 1 {
		t.Errorf("oldapp: %+v", f)
	}
	if f := got[away]; f.Dataset != "tank/away/farapp" || f.Name != "farapp" {
		t.Errorf("farapp: %+v", f)
	}

	rec := httptest.NewRecorder()
	s.handleLeftovers(rec, httptest.NewRequest("POST", "/api/maintenance/leftovers", strings.NewReader(`{"path":"`+old+`"}`)))
	if rec.Code != 204 {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	want := []string{"kept tank/apps/oldapp.kept-1", "tank/apps/oldapp for oldapp"}
	if strings.Join(z.destroyed, "|") != strings.Join(want, "|") {
		t.Errorf("destroyed %v, want %v", z.destroyed, want)
	}
	if _, err := os.Stat(filepath.Join(old, "config/db")); err != nil {
		t.Error("a dataset was emptied file by file")
	}
}

// Starting fresh moves a plain folder aside, records when it goes, and takes
// its secrets along; nothing is deleted. A folder in use is refused.
func TestSetAsidePlainFolder(t *testing.T) {
	s, root := leftoverFixture(t)
	old := filepath.Join(root, "apps/oldapp")
	at := time.Date(2026, 11, 20, 8, 30, 0, 0, time.UTC)
	recordKept(root, keptRecord{Path: old, Name: "oldapp", AppID: "oldapp", DeletedAt: at, Version: "1.4.2"})
	writeFile(t, secretsPath(root, old), "P=x\n")

	note, err := s.setAsideKeptData(context.Background(), old, "oldapp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("the folder is still in place")
	}
	recs := loadKeptRecords(root)
	if len(recs) != 1 || !recs[0].SetAside || recs[0].Until == nil || recs[0].Version != "1.4.2" || !recs[0].DeletedAt.Equal(at) {
		t.Fatalf("records: %+v", recs)
	}
	aside := recs[0].Path
	if !strings.HasPrefix(aside, old+".kept-") || !dataset.IsInternalDir(filepath.Base(aside)) {
		t.Errorf("set aside as %s", aside)
	}
	if _, err := os.Stat(filepath.Join(aside, "config/db")); err != nil {
		t.Error("its data did not move with it")
	}
	if _, err := os.Stat(secretsPath(root, aside)); err != nil {
		t.Error("its secrets did not move with it")
	}
	if _, err := os.Stat(secretsPath(root, old)); err == nil {
		t.Error("its secrets stayed for the new install")
	}
	if d := time.Until(*recs[0].Until); d < keptCopyTTL-time.Minute || d > keptCopyTTL+time.Minute {
		t.Errorf("kept for %s, want %s", d, keptCopyTTL)
	}
	if !strings.Contains(note, aside) {
		t.Errorf("note does not name the folder: %q", note)
	}

	// Listed as set aside, with the day it goes, and removable by hand.
	f, ok := leftoverAt(t, s, aside)
	if !ok || !f.SetAside || f.Until == nil || f.Installable {
		t.Fatalf("set-aside row: %+v (listed %v)", f, ok)
	}

	// In use: a stack's own folder, and one a container mounts.
	for _, used := range []string{"apps/notes", "apps/ansible-svc"} {
		if _, err := s.setAsideKeptData(context.Background(), filepath.Join(root, used), "x"); !errors.Is(err, errKeptInUse) {
			t.Errorf("%s: %v, want in use", used, err)
		}
	}
	// Nothing there: nothing to do.
	if note, err := s.setAsideKeptData(context.Background(), filepath.Join(root, "apps/none"), "none"); err != nil || note != "" {
		t.Errorf("no folder: %q %v", note, err)
	}
}

// Starting fresh on a dataset makes it a kept copy of the stack: fjord's own
// kept-copy sweep removes it, so nothing is recorded for it.
func TestSetAsideDataset(t *testing.T) {
	s, root := leftoverFixture(t)
	old := filepath.Join(root, "apps/oldapp")
	recordKept(root, keptRecord{Path: old, Name: "oldapp", DeletedAt: time.Now(), Version: "1.4.2"})
	z := &fakeZFS{live: []dataset.LiveDataset{{Name: "tank/apps/oldapp", Stack: "oldapp", Mountpoint: old}}}
	z.install(t)

	if _, err := s.setAsideKeptData(context.Background(), old, "oldapp"); err != nil {
		t.Fatal(err)
	}
	want := "tank/apps/oldapp oldapp " + dataset.KindFreshInstall + " 1.4.2"
	if len(z.setAside) != 1 || z.setAside[0] != want {
		t.Errorf("set aside %v, want %q", z.setAside, want)
	}
	if recs := loadKeptRecords(root); len(recs) != 0 {
		t.Errorf("records: %+v", recs)
	}
}

// The sweep removes a set-aside folder when its time is up, not before, and
// never a folder that is not named as one fjord set aside.
func TestSweepSetAside(t *testing.T) {
	s, root := leftoverFixture(t)
	apps := filepath.Join(root, "apps")
	due := filepath.Join(apps, "a.kept-2026-10-01-000000")
	later := filepath.Join(apps, "b.kept-2026-11-27-000000")
	odd := filepath.Join(apps, "oldapp")
	os.MkdirAll(filepath.Join(due, "data"), 0o755)
	os.MkdirAll(later, 0o755)
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	recordKept(root, keptRecord{Path: due, Name: "a", SetAside: true, Until: &past})
	recordKept(root, keptRecord{Path: later, Name: "b", SetAside: true, Until: &future})
	recordKept(root, keptRecord{Path: odd, Name: "oldapp", SetAside: true, Until: &past})
	writeFile(t, secretsPath(root, due), "P=x\n")

	s.sweepSetAside(now)
	if _, err := os.Stat(due); err == nil {
		t.Error("the due folder is still there")
	}
	if _, err := os.Stat(secretsPath(root, due)); err == nil {
		t.Error("its secrets are still there")
	}
	for _, stays := range []string{later, odd} {
		if _, err := os.Stat(stays); err != nil {
			t.Errorf("%s was removed", stays)
		}
	}
	recs := loadKeptRecords(root)
	if len(recs) != 1 || recs[0].Path != later {
		t.Errorf("records after the sweep: %+v", recs)
	}
}

// The version comes from the app's own image when the stack has several.
func TestAppVersionOf(t *testing.T) {
	s, root := deleteFixture(t)
	writeFile(t, filepath.Join(root, "stacks/multi/compose.yaml"),
		"services:\n  cache:\n    image: docker.io/library/redis:7\n  app:\n    image: ghcr.io/acme/multi:latest\n")
	writeFile(t, filepath.Join(root, "stacks/multi/state.json"), `{"engine":"podman","origin":{"type":"catalog","app_id":"multi"}}`)
	s.backends["podman"] = versionedBackend{&cliMockBackend{stopped: true}, map[string]string{
		"docker.io/library/redis:7": "7.2.4", "ghcr.io/acme/multi:latest": "3.1.0",
	}}
	st, err := s.manager.Get("multi")
	if err != nil {
		t.Fatal(err)
	}
	if v := s.appVersionOf(context.Background(), st); v != "3.1.0" {
		t.Errorf("version %q, want the app's 3.1.0", v)
	}
	// An engine that cannot read labels: unknown.
	s.backends["podman"] = engine.Unavailable("podman")
	if v := s.appVersionOf(context.Background(), st); v != "" {
		t.Errorf("version %q from an engine with no labels", v)
	}
}

// Data kept in a dataset fjord made for this name is the new install's own
// dataset again; a plain folder or someone else's dataset is not.
func TestKeptDatasetAt(t *testing.T) {
	oldAt, oldOwned := datasetAtFn, datasetOwnedFn
	t.Cleanup(func() { datasetAtFn, datasetOwnedFn = oldAt, oldOwned })
	datasetAtFn = func(_ context.Context, dir string) (string, bool) {
		if dir == "/apps/radarr" || dir == "/apps/theirs" {
			return "tank" + dir, true
		}
		return "", false
	}
	datasetOwnedFn = func(_ context.Context, name, stack string) error {
		if name == "tank/apps/radarr" && stack == "radarr" {
			return nil
		}
		return errors.New("not fjord's")
	}
	ctx := context.Background()
	if ds := keptDatasetAt(ctx, "/apps/radarr", "radarr"); ds != "tank/apps/radarr" {
		t.Errorf("its own dataset: %q", ds)
	}
	if ds := keptDatasetAt(ctx, "/apps/theirs", "theirs"); ds != "" {
		t.Errorf("someone else's dataset taken: %q", ds)
	}
	if ds := keptDatasetAt(ctx, "/apps/plain", "plain"); ds != "" {
		t.Errorf("a plain folder taken for a dataset: %q", ds)
	}
}
