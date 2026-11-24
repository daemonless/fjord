package dataset

import (
	"context"
	"errors"
	"strings"
	"testing"
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
		for prefix, out := range f.answers {
			if strings.HasPrefix(cmd, prefix) {
				if strings.HasPrefix(out, "ERR ") {
					return nil, errors.New(out[4:])
				}
				return []byte(out), nil
			}
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
