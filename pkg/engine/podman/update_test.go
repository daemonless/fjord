package podman

import (
	"reflect"
	"testing"
)

// A partial update force-removes and starts only the services it names. The
// whole-stack list is what a refused recreate used to remove, taking immich's
// database down to update its ML image.
func TestOfServices(t *testing.T) {
	cs := []libpodContainer{
		{Names: []string{"immich_server_1"}, Labels: map[string]string{"io.podman.compose.service": "immich-server"}},
		{Names: []string{"immich_database_1"}, Labels: map[string]string{"io.podman.compose.service": "database"}},
		{Names: []string{"immich_redis_1"}, Labels: map[string]string{"io.podman.compose.service": "redis"}},
	}
	if got := containerNames(ofServices(cs, []string{"database"})); !reflect.DeepEqual(got, []string{"immich_database_1"}) {
		t.Errorf("database only: %v", got)
	}
	if got := containerNames(ofServices(cs, nil)); len(got) != 3 {
		t.Errorf("no filter should be the whole stack, got %v", got)
	}
	if got := ofServices(cs, []string{"nope"}); len(got) != 0 {
		t.Errorf("unknown service matched %v", got)
	}
}

// Measured on saturn with podman-compose 1.5.0: `up --remove-orphans
// --no-deps b` deletes a's container, because a service left off the list
// counts as an orphan. A partial update must never carry the flag.
func TestUpArgsPartialNeverRemovesOrphans(t *testing.T) {
	for _, a := range upArgs(recreateAll, []string{"database"}) {
		if a == "--remove-orphans" {
			t.Fatalf("partial update carries --remove-orphans: %v", upArgs(recreateAll, []string{"database"}))
		}
	}
	got := upArgs(recreateAll, []string{"database"})
	want := []string{"--in-pod=false", "up", "-d", "--force-recreate", "--no-deps", "database"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("partial: %v, want %v", got, want)
	}
	// The whole stack still clears services dropped from the compose.
	if got := upArgs(recreateChanged, nil); !reflect.DeepEqual(got, []string{"--in-pod=false", "up", "-d", "--remove-orphans"}) {
		t.Errorf("whole stack: %v", got)
	}
}

// Read in podman-compose 1.5.0 (compose_up): a plain `up` tears down EVERY
// container when any one's config hash differs from the file's -- which is
// the case for all the others once Apply recreated one. Starting the rest
// after it must not recreate: crafting-apps' 12 other apps were all re-made
// for a change to photocraft.
func TestUpArgsTheRestRecreatesNothing(t *testing.T) {
	got := upArgs(recreateNone, nil)
	want := []string{"--in-pod=false", "up", "-d", "--remove-orphans", "--no-recreate"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the rest: %v, want %v", got, want)
	}
}
