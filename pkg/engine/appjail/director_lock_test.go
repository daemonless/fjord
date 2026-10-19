package appjail

import "testing"

// A director lock left by a killed run wedged the stack's name for good:
// every up and down refused with ProjectLocked.
func TestStaleDirectorLock(t *testing.T) {
	refused := []byte("Exception:\n type: ProjectLocked\n error: t-pj: Project locked. Run `rm -f /tmp/director/locks/t-pj/lock` if you are sure that no other process is locking this project.\n")
	running := false
	old := directorRunningIn
	directorRunningIn = func(string) bool { return running }
	t.Cleanup(func() { directorRunningIn = old })

	if got := staleDirectorLock(refused, "/var/db/fjord/stacks/t-pj"); got != "/tmp/director/locks/t-pj/lock" {
		t.Errorf("no director running: got %q, want the lock", got)
	}
	running = true
	if got := staleDirectorLock(refused, "/var/db/fjord/stacks/t-pj"); got != "" {
		t.Errorf("a director running here: got %q, want it left alone", got)
	}
	running = false
	if got := staleDirectorLock([]byte("Project not found"), "/x"); got != "" {
		t.Errorf("another failure: got %q", got)
	}
	if got := staleDirectorLock([]byte("ProjectLocked ... rm -f /etc/lock "), "/x"); got != "" {
		t.Errorf("a lock outside director's folder: got %q", got)
	}
}
