package dataset

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestListLiveSkipsKeptCopiesAndUntagged(t *testing.T) {
	(&fake{answers: map[string]string{
		"list -H -p -o name,mountpoint,used,fjord:stack,fjord:kept-kind": "" +
			"tank\t/tank\t900\t-\t-\n" +
			"tank/apps/radarr\t/apps/radarr\t500\tradarr\t-\n" +
			"tank/apps/radarr.kept-2026-11-27-010203\t/apps/radarr.kept-2026-11-27-010203\t40\tradarr\trollback\n" +
			"tank/apps/quiet\tnone\t7\tquiet\t-\n",
	}}).install(t)
	got, err := ListLive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0] != (LiveDataset{Name: "tank/apps/radarr", Stack: "radarr", Mountpoint: "/apps/radarr", UsedBytes: 500}) {
		t.Errorf("radarr: %+v", got[0])
	}
	if got[1].Name != "tank/apps/quiet" || got[1].Mountpoint != "" {
		t.Errorf("unmounted: %+v", got[1])
	}
}

func TestListLiveNoZFS(t *testing.T) {
	(&fake{answers: map[string]string{"list": "ERR zfs: not found"}}).install(t)
	if got, err := ListLive(context.Background()); err == nil || got != nil {
		t.Fatalf("got %+v %v", got, err)
	}
}

// Set aside: renamed, read-only, tagged as a kept copy that expires, and no
// new live dataset made.
func TestSetAside(t *testing.T) {
	f := &fake{answers: map[string]string{
		"list -H -r -o name,fjord:stack": "tank/apps/radarr\tradarr\n",
	}}
	f.install(t)
	now := time.Date(2026, 11, 27, 1, 2, 3, 0, time.UTC)
	kept, err := SetAside(context.Background(), "tank/apps/radarr", "radarr", KindFreshInstall, "2.1.6", now, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if kept != "tank/apps/radarr.kept-2026-11-27-010203" {
		t.Errorf("kept as %q", kept)
	}
	ran := strings.Join(f.ran, "\n")
	for _, want := range []string{
		"rename tank/apps/radarr " + kept,
		"readonly=on",
		Tag + "=radarr",
		PropKeptKind + "=" + KindFreshInstall,
		PropKeptUntil + "=2026-12-27T01:02:03Z",
		PropKeptVersion + "=2.1.6",
	} {
		if !strings.Contains(ran, want) {
			t.Errorf("did not run %q:\n%s", want, ran)
		}
	}
	for _, never := range []string{"clone", "create", "destroy", "promote"} {
		if strings.Contains(ran, "\n"+never+" ") {
			t.Errorf("ran %q:\n%s", never, ran)
		}
	}
}

// A mountpoint set by hand moves with the kept copy, so the name is free for
// the fresh dataset.
func TestSetAsideLocalMountpoint(t *testing.T) {
	f := &fake{answers: map[string]string{
		"list -H -r -o name,fjord:stack":                                         "tank/apps/radarr\tradarr\n",
		"get -H -o property,value,source -s local,received all tank/apps/radarr": "mountpoint\t/data/radarr\tlocal\n",
	}}
	f.install(t)
	now := time.Date(2026, 11, 27, 1, 2, 3, 0, time.UTC)
	if _, err := SetAside(context.Background(), "tank/apps/radarr", "radarr", KindFreshInstall, "", now, time.Hour); err != nil {
		t.Fatal(err)
	}
	if ran := strings.Join(f.ran, "\n"); !strings.Contains(ran, "set mountpoint=/data/radarr.kept-2026-11-27-010203 tank/apps/radarr.kept-2026-11-27-010203") {
		t.Errorf("mountpoint not moved:\n%s", ran)
	}
}

// Someone else's dataset is never set aside.
func TestSetAsideOnlyWhatFjordMade(t *testing.T) {
	f := &fake{answers: map[string]string{
		"list -H -r -o name,fjord:stack": "tank/apps/radarr\t-\n",
	}}
	f.install(t)
	if _, err := SetAside(context.Background(), "tank/apps/radarr", "radarr", KindFreshInstall, "", time.Now(), time.Hour); err == nil {
		t.Fatal("set aside a dataset fjord did not make")
	}
	if ran := strings.Join(f.ran, "\n"); strings.Contains(ran, "rename") {
		t.Errorf("renamed anyway:\n%s", ran)
	}
}
