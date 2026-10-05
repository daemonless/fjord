package podman

import (
	"testing"
	"time"
)

// The up is stuck only once a container has been failed for the whole grace
// AND compose has been quiet for it: saturn 2026-10-04, db exited 1 and
// podman-compose sat in `podman wait` with nothing more to say.
func TestDepWatchStuck(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	db := failedContainer{id: "db1", service: "db", why: "exited with code 1"}
	d := &depWatch{grace: 20 * time.Second}

	if got := d.observe(t0, []failedContainer{db}, t0); got != nil {
		t.Fatalf("just failed: %v", got)
	}
	// Failed for 10 s, compose quiet since t0: not yet.
	if got := d.observe(t0.Add(10*time.Second), []failedContainer{db}, t0); got != nil {
		t.Fatalf("10 s: %v", got)
	}
	// 20 s failed and 20 s quiet: stuck on db.
	got := d.observe(t0.Add(20*time.Second), []failedContainer{db}, t0)
	if len(got) != 1 || got[0].service != "db" {
		t.Fatalf("20 s: %v", got)
	}
}

// Compose still printing means it is still working (starting a stopped
// stack, whose containers read "exited" until it gets to them).
func TestDepWatchComposeTalking(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	db := failedContainer{id: "db1", service: "db"}
	d := &depWatch{grace: 20 * time.Second}
	d.observe(t0, []failedContainer{db}, t0)
	if got := d.observe(t0.Add(30*time.Second), []failedContainer{db}, t0.Add(15*time.Second)); got != nil {
		t.Fatalf("compose printed 15 s ago: %v", got)
	}
}

// A container that ran in between starts over: a crash loop podman restarts
// can still come up, and podman wait sees it running then.
func TestDepWatchRecoveredStartsOver(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	db := failedContainer{id: "db1", service: "db"}
	d := &depWatch{grace: 20 * time.Second}
	d.observe(t0, []failedContainer{db}, t0)
	d.observe(t0.Add(15*time.Second), nil, t0) // running at this poll
	if got := d.observe(t0.Add(25*time.Second), []failedContainer{db}, t0); got != nil {
		t.Fatalf("failed again only 0 s ago: %v", got)
	}
	if got := d.observe(t0.Add(45*time.Second), []failedContainer{db}, t0); len(got) != 1 {
		t.Fatalf("failed 20 s since it ran: %v", got)
	}
}
