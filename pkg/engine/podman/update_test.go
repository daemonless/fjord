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
	for _, a := range upArgs(true, []string{"database"}) {
		if a == "--remove-orphans" {
			t.Fatalf("partial update carries --remove-orphans: %v", upArgs(true, []string{"database"}))
		}
	}
	got := upArgs(true, []string{"database"})
	want := []string{"--in-pod=false", "up", "-d", "--force-recreate", "--no-deps", "database"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("partial: %v, want %v", got, want)
	}
	// The whole stack still clears services dropped from the compose.
	if got := upArgs(false, nil); !reflect.DeepEqual(got, []string{"--in-pod=false", "up", "-d", "--remove-orphans"}) {
		t.Errorf("whole stack: %v", got)
	}
}
