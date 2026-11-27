package dataset

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRestoreWithKeepPreservesLocalPropsAndTagsBeforeClone(t *testing.T) {
	ds := "zroot/containers/app"
	snap := ds + "@fjord-update-1795774303"
	now := time.Date(2026, 11, 27, 5, 30, 0, 0, time.UTC)
	kept := ds + ".kept-2026-11-27-053000"

	f := &fake{answers: map[string]string{
		"list -H -r -o name,fjord:stack": ds + "\tapp\n",
		"get -H -o property,value,source -s local,received all " + ds: strings.Join([]string{
			"fjord:stack\tapp\tlocal",
			"compression\tzstd\tlocal",
			"quota\t5G\treceived",
			"used\t12345\tlocal",
		}, "\n") + "\n",
	}}
	f.install(t)

	got, err := RestoreWithKeep(context.Background(), ds, snap, "app", "2.1.7", "ghcr.io/x/app@sha256:abc", now, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got != kept {
		t.Fatalf("got %q, want %q", got, kept)
	}

	renameIdx, setIdx, cloneIdx, promoteIdx := -1, -1, -1, -1
	for i, c := range f.ran {
		switch {
		case c == "rename "+ds+" "+kept:
			renameIdx = i
		case strings.HasPrefix(c, "set readonly=on "):
			setIdx = i
			if !strings.Contains(c, "fjord:kept-version=2.1.7") || !strings.Contains(c, "fjord:kept-image=ghcr.io/x/app@sha256:abc") {
				t.Errorf("kept props missing: %s", c)
			}
		case strings.HasPrefix(c, "clone "):
			cloneIdx = i
			if !strings.Contains(c, "-o compression=zstd") || !strings.Contains(c, "-o quota=5G") {
				t.Errorf("clone lost local/received props: %s", c)
			}
			if !strings.Contains(c, kept+"@fjord-update-1795774303 "+ds) {
				t.Errorf("clone args wrong: %s", c)
			}
		case c == "promote "+ds:
			promoteIdx = i
		}
	}
	// setKeptProps MUST run right after rename (before clone/promote) so a crash
	// after promote never leaves an untagged kept dataset.
	if !(renameIdx >= 0 && setIdx > renameIdx && cloneIdx > setIdx && promoteIdx > cloneIdx) {
		t.Fatalf("wrong command order: rename=%d set=%d clone=%d promote=%d\nran=%v", renameIdx, setIdx, cloneIdx, promoteIdx, f.ran)
	}
}

func TestRestoreWithKeepRollsBackRenameOnCloneFailure(t *testing.T) {
	ds := "zroot/containers/app"
	snap := ds + "@fjord-update-1795774303"
	now := time.Date(2026, 11, 27, 5, 30, 0, 0, time.UTC)
	kept := ds + ".kept-2026-11-27-053000"

	f := &fake{answers: map[string]string{
		"list -H -r -o name,fjord:stack":                              ds + "\tapp\n",
		"get -H -o property,value,source -s local,received all " + ds: "fjord:stack\tapp\tlocal\n",
		"clone ": "ERR clone failed",
	}}
	f.install(t)

	if _, err := RestoreWithKeep(context.Background(), ds, snap, "app", "2.1.7", "", now, 30*24*time.Hour); err == nil {
		t.Fatal("expected error")
	}
	if f.ran[len(f.ran)-1] != "rename -f "+kept+" "+ds {
		t.Fatalf("did not undo rename on clone failure: %v", f.ran)
	}
}

func TestDestroyKeptRefusesLiveDataset(t *testing.T) {
	ds := "zroot/containers/app"
	f := &fake{answers: map[string]string{
		"list -H -r -o name,fjord:stack":        ds + "\tapp\n",
		"get -H -o value fjord:kept-kind " + ds: "-\n",
	}}
	f.install(t)

	if _, err := DestroyKept(context.Background(), ds, "app"); err == nil {
		t.Fatal("DestroyKept allowed destroying a live dataset")
	}
	if err := SetKeptForever(context.Background(), ds, "app", true, time.Now()); err == nil {
		t.Fatal("SetKeptForever allowed modifying a live dataset")
	}
	for _, c := range f.ran {
		if strings.HasPrefix(c, "destroy ") {
			t.Fatalf("ran destroy on live dataset: %v", f.ran)
		}
	}
}

func TestDestroyKeptPromotesDependentCloneFirst(t *testing.T) {
	ds := "zroot/containers/app"
	kept1 := ds + ".kept-2026-11-27-053000"
	kept2 := ds + ".kept-2026-11-27-060000"

	f := &fake{answers: map[string]string{
		"list -H -r -o name,fjord:stack":              kept2 + "\tapp\n",
		"get -H -o value fjord:kept-kind " + kept2:    "rollback\n",
		"list -H -p -o used " + kept2:                 "4096\n",
		"list -H -t snapshot -o clones -d 1 " + kept2: kept1 + "\n",
	}}
	f.install(t)

	freed, err := DestroyKept(context.Background(), kept2, "app")
	if err != nil {
		t.Fatal(err)
	}
	if freed != 4096 {
		t.Fatalf("freed=%d, want 4096", freed)
	}
	promoteIdx, destroyIdx := -1, -1
	for i, c := range f.ran {
		if c == "promote "+kept1 {
			promoteIdx = i
		}
		if c == "destroy -f -r "+kept2 {
			destroyIdx = i
		}
	}
	if !(promoteIdx >= 0 && destroyIdx > promoteIdx) {
		t.Fatalf("expected promote before destroy: %v", f.ran)
	}
}

func TestRestoreKeptDestructiveSwapRollsBackOnFinalRenameFailure(t *testing.T) {
	ds := "zroot/containers/app"
	kept := ds + ".kept-2026-11-27-053000"
	now := time.Unix(1795774600, 0).UTC()
	drop := ds + ".deleting-1795774600"

	f := &fake{answers: map[string]string{
		"list -H -r -o name,fjord:stack":          ds + "\tapp\n" + kept + "\tapp\n",
		"get -H -o value fjord:kept-kind " + kept: "rollback\n",
		"get -H -o property,value,source":         "fjord:stack\tapp\tlocal\n",
		"list -H -p -t snapshot":                  "",
		"rename -f " + kept + " " + ds:            "ERR target busy",
	}}
	f.install(t)

	if _, err := RestoreKept(context.Background(), ds, kept, "app", "2.1.6", "", false, now, 30*24*time.Hour); err == nil {
		t.Fatal("expected error")
	}
	var restoredDrop, destroyedDrop bool
	for _, c := range f.ran {
		if c == "rename -f "+drop+" "+ds {
			restoredDrop = true
		}
		if strings.HasPrefix(c, "destroy ") {
			destroyedDrop = true
		}
	}
	if !restoredDrop || destroyedDrop {
		t.Fatalf("unsafe rollback on final rename failure: %v", f.ran)
	}
}

func TestReconcilePendingRecoversMissingLiveDataset(t *testing.T) {
	ds := "zroot/containers/app"
	kept := ds + ".kept-2026-11-27-053000"
	newKept := ds + ".kept-2026-11-27-061000"
	f := &fake{answers: map[string]string{
		"list -H -o name -t filesystem " + ds:      "ERR does not exist",
		"list -H -o name -t filesystem " + kept:    kept + "\n",
		"list -H -o name -t filesystem " + newKept: newKept + "\n",
	}}
	f.install(t)

	note := ReconcilePending(context.Background(), ds, kept, newKept, "", "")
	if note == "" {
		t.Fatal("expected recovery note")
	}
	var renamed bool
	for _, c := range f.ran {
		if c == "rename -f "+newKept+" "+ds {
			renamed = true
		}
	}
	if !renamed {
		t.Fatalf("did not recover live dataset from newKept: %v", f.ran)
	}

	// Live exists + drop exists: must clear fjord:kept-* on live before
	// destroying drop so live is never left marked as a kept copy.
	drop := ds + ".deleting-1795774600"
	f2 := &fake{answers: map[string]string{
		"list -H -o name -t filesystem " + ds:   ds + "\n",
		"list -H -o name -t filesystem " + drop: drop + "\n",
	}}
	f2.install(t)
	if note := ReconcilePending(context.Background(), ds, kept, "", drop, ""); note == "" {
		t.Fatal("expected cleanup note")
	}
	var cleared, destroyed bool
	for _, c := range f2.ran {
		if c == "inherit fjord:kept-kind "+ds {
			cleared = true
		}
		if c == "destroy -f -r "+drop {
			destroyed = true
		}
	}
	if !cleared || !destroyed {
		t.Fatalf("expected clearKeptProps on live and destroy drop, ran=%v", f2.ran)
	}
}

func TestSnapshotManualAndDestroy(t *testing.T) {
	ds := "zroot/containers/app"
	now := time.Unix(1795774500, 0).UTC()
	wantSnap := ds + "@fjord-manual-1795774500"

	f := &fake{answers: map[string]string{
		"list -H -r -o name,fjord:stack": ds + "\tapp\n",
		"list -H -p -o used " + wantSnap: "8192\n",
	}}
	f.install(t)

	got, err := SnapshotManual(context.Background(), ds, "app", "Before config tweak", now, `{"app":"ghcr.io/x/app:2.1.7@sha256:abc"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantSnap {
		t.Fatalf("got %q, want %q", got, wantSnap)
	}
	lastCmd := f.ran[len(f.ran)-1]
	if !strings.Contains(lastCmd, "fjord:note=Before config tweak") || !strings.Contains(lastCmd, "fjord:images=") {
		t.Fatalf("ran %q", lastCmd)
	}

	if _, err := DestroyManualSnapshot(context.Background(), ds, ds+"@fjord-update-1795774303", "app"); err == nil {
		t.Fatal("expected refusal on update snapshot")
	}

	freed, err := DestroyManualSnapshot(context.Background(), ds, wantSnap, "app")
	if err != nil {
		t.Fatal(err)
	}
	if freed != 8192 {
		t.Fatalf("freed=%d, want 8192", freed)
	}
	if f.ran[len(f.ran)-1] != "destroy -d "+wantSnap {
		t.Fatalf("ran %q, want destroy -d %s", f.ran[len(f.ran)-1], wantSnap)
	}
}
