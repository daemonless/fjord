package dataset

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fake answers zfs commands from a table and records what ran.
type fake struct {
	answers map[string]string
	ran     []string
}

func (f *fake) install(t *testing.T) {
	old := run
	run = func(_ context.Context, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		f.ran = append(f.ran, cmd)
		var bestPrefix, bestOut string
		matched := false
		for prefix, out := range f.answers {
			if strings.HasPrefix(cmd, prefix) && (!matched || len(prefix) > len(bestPrefix)) {
				bestPrefix, bestOut, matched = prefix, out, true
			}
		}
		if matched {
			if strings.HasPrefix(bestOut, "ERR ") {
				return nil, errors.New(bestOut[4:])
			}
			return []byte(bestOut), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { run = old })
}

const list = "zroot\tnone\nzroot/ROOT/default\t/\nzroot/containers\t/containers\nzroot/var\t/var\n"

func TestAt(t *testing.T) {
	(&fake{answers: map[string]string{"list -H -o name,mountpoint": list}}).install(t)
	if ds, ok := At(context.Background(), "/containers/"); !ok || ds != "zroot/containers" {
		t.Fatalf("got %q %v", ds, ok)
	}
	// A folder inside a dataset is not its mountpoint.
	if _, ok := At(context.Background(), "/var/db/fjord/containers"); ok {
		t.Fatal("a plain folder was taken for a dataset")
	}
}

func TestAtNoZFS(t *testing.T) {
	(&fake{answers: map[string]string{"list": "ERR zfs: not found"}}).install(t)
	if _, ok := At(context.Background(), "/containers"); ok {
		t.Fatal("no zfs, yet a dataset")
	}
}

func TestLiveByTagSkipsKeptCopies(t *testing.T) {
	out := strings.Join([]string{
		"zroot/containers/app.kept-2026-11-27-0530\tapp\trollback",
		"zroot/containers/app\tapp\t-",
		"zroot/containers/other\tother\t-",
	}, "\n") + "\n"
	(&fake{answers: map[string]string{"list -H -o name,fjord:stack,fjord:kept-kind": out}}).install(t)

	got, ok := LiveByTag(context.Background(), "app")
	if !ok || got != "zroot/containers/app" {
		t.Fatalf("got %q %v, want zroot/containers/app", got, ok)
	}

	// Only a kept copy exists: must not return it as the live dataset.
	onlyKept := "zroot/containers/app.kept-2026-11-27-0530\tapp\trollback\n"
	(&fake{answers: map[string]string{"list -H -o name,fjord:stack,fjord:kept-kind": onlyKept}}).install(t)
	if got, ok := LiveByTag(context.Background(), "app"); ok {
		t.Fatalf("returned kept copy %q as live dataset", got)
	}
}

func TestCreate(t *testing.T) {
	f := &fake{}
	f.install(t)
	name, err := Create(context.Background(), "zroot/containers", "immich")
	if err != nil || name != "zroot/containers/immich" {
		t.Fatalf("%q %v", name, err)
	}
	if f.ran[0] != "create -o fjord:stack=immich zroot/containers/immich" {
		t.Fatalf("ran %q", f.ran[0])
	}
	if _, err := Create(context.Background(), "zroot/containers", "a/b"); err == nil {
		t.Fatal("a path made it into a dataset name")
	}
}

func TestDestroyOnlyWhatFjordMade(t *testing.T) {
	ctx := context.Background()
	mine := &fake{answers: map[string]string{"list -H -r": "zroot/containers/immich\timmich\nzroot/containers/immich/db\timmich\n"}}
	mine.install(t)
	if err := Destroy(ctx, "zroot/containers/immich", "immich"); err != nil {
		t.Fatal(err)
	}
	if mine.ran[len(mine.ran)-1] != "destroy -r zroot/containers/immich" {
		t.Fatalf("ran %v", mine.ran)
	}

	foreign := &fake{answers: map[string]string{"list -H -r": "zroot/containers/immich\timmich\nzroot/containers/immich/extra\t-\n"}}
	foreign.install(t)
	err := Destroy(ctx, "zroot/containers/immich", "immich")
	if err == nil || !strings.Contains(err.Error(), "zroot/containers/immich/extra wasn't made by fjord") {
		t.Fatalf("got %v", err)
	}
	for _, c := range foreign.ran {
		if strings.HasPrefix(c, "destroy") {
			t.Fatal("destroyed with a child fjord did not make")
		}
	}

	other := &fake{answers: map[string]string{"list -H -r": "zroot/containers/immich\tsomething-else\n"}}
	other.install(t)
	if err := Destroy(ctx, "zroot/containers/immich", "immich"); err == nil {
		t.Fatal("destroyed a dataset tagged for another stack")
	}
}

func TestSnapPredicatesAndInternalDirs(t *testing.T) {
	ds := "zroot/containers/app"
	if !IsUpdateSnap(ds, ds+"@fjord-update-1795774303") {
		t.Error("new update snap not recognized")
	}
	if !IsUpdateSnap(ds, ds+"@fjord-1795774303") {
		t.Error("legacy update snap not recognized")
	}
	if IsUpdateSnap(ds, ds+"@fjord-manual-1795774303") {
		t.Error("manual snap misclassified as update snap")
	}
	if !IsManualSnap(ds, ds+"@fjord-manual-1795774303") {
		t.Error("manual snap not recognized")
	}
	if IsFjordSnap(ds, ds+"@user-backup") {
		t.Error("non-fjord snap matched")
	}
	for _, internal := range []string{
		"app.kept-2026-11-27-0530",
		"app.pre-dataset-20261127-0530",
		"app.deleting-1795774600",
	} {
		if !IsInternalDir(internal) {
			t.Errorf("%q not recognized as internal dir", internal)
		}
	}
	if IsInternalDir("app") || IsInternalDir("my-kept-notes") {
		t.Error("normal folder misclassified as internal dir")
	}
}

func TestSnapshotPrunerSkipsManualDeferredAndCloneHeld(t *testing.T) {
	ds := "zroot/containers/app"
	snapList := strings.Join([]string{
		ds + "@user-snap\t1700000000\t1024\toff\t-",
		ds + "@fjord-1700000100\t1700000100\t2048\ton\t" + ds + ".kept-old",
		ds + "@fjord-update-1700000200\t1700000200\t2048\toff\t" + ds + ".kept-active",
		ds + "@fjord-manual-1700000250\t1700000250\t4096\toff\t-",
		ds + "@fjord-update-1700000300\t1700000300\t1000\toff\t-",
		ds + "@fjord-update-1700000400\t1700000400\t1000\toff\t-",
		ds + "@fjord-update-1700000500\t1700000500\t1000\toff\t-",
		ds + "@fjord-update-1700000600\t1700000600\t1000\toff\t-",
		ds + "@fjord-update-1700000700\t1700000700\t1000\toff\t-",
	}, "\n") + "\n"

	f := &fake{answers: map[string]string{
		"list -H -p -t snapshot": snapList,
		// Simulate first prune failing: loop must still prune the second excess snap.
		"destroy -d " + ds + "@fjord-update-1700000300": "ERR dataset is busy",
	}}
	f.install(t)

	got, err := Snapshot(context.Background(), ds, time.Unix(1700000700, 0), 3)
	if err != nil {
		t.Fatal(err)
	}
	if got != ds+"@fjord-update-1700000700" {
		t.Fatalf("got %q", got)
	}

	var destroyed []string
	for _, c := range f.ran {
		if strings.HasPrefix(c, "destroy ") {
			destroyed = append(destroyed, c)
		}
	}
	// 5 countable update snaps (300..700), keep 3 -> must prune 300 and 400 with -d,
	// never touch manual (250), deferred (100), clone-held (200), or user-snap.
	want := []string{
		"destroy -d " + ds + "@fjord-update-1700000300",
		"destroy -d " + ds + "@fjord-update-1700000400",
	}
	if strings.Join(destroyed, "|") != strings.Join(want, "|") {
		t.Fatalf("destroyed %v, want %v", destroyed, want)
	}

	// ListSnapshots with includeDeferred=false must hide the defer_destroy=on snap.
	visible, err := ListSnapshots(context.Background(), ds, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range visible {
		if s.DeferDestroy {
			t.Fatalf("deferred snapshot %s leaked into UI list", s.Name)
		}
	}
}

func TestRollbackRefusesWhenCloneDependsOnNewerSnapshot(t *testing.T) {
	ds := "zroot/containers/app"
	snapList := strings.Join([]string{
		ds + "@fjord-update-1700000100\t1700000100\t1000\toff\t-",
		ds + "@fjord-update-1700000200\t1700000200\t2000\toff\t" + ds + ".kept-2026-11-27-0530",
	}, "\n") + "\n"
	f := &fake{answers: map[string]string{"list -H -p -t snapshot": snapList}}
	f.install(t)

	err := Rollback(context.Background(), ds, ds+"@fjord-update-1700000100")
	if err == nil || !strings.Contains(err.Error(), ".kept-2026-11-27-0530") {
		t.Fatalf("expected clone refusal, got %v", err)
	}
	for _, c := range f.ran {
		if strings.HasPrefix(c, "rollback") {
			t.Fatal("ran zfs rollback despite clone dependency")
		}
	}
}
