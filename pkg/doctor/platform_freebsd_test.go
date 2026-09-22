package doctor

import (
	"strings"
	"testing"
	"time"
)

// The real numbers from jupiter, 2026-09-22. podman_service had been up since
// Sep 6; `pkg upgrade` replaced podman under it on Sep 21. Every stack's shell
// returned 500 "no such file or directory" from the API while `podman exec` on
// the command line worked, so it read as a fjord bug rather than a host one.
func TestServiceStaleCatchesTheJupiterIncident(t *testing.T) {
	started := time.Date(2026, 9, 6, 0, 23, 12, 0, time.Local)
	installed := time.Date(2026, 9, 21, 18, 28, 0, 0, time.Local)

	st, msg := serviceStale(started, installed, "podman")
	if st != Warn {
		t.Fatalf("status = %v, want Warn: a service older than its package is the whole bug", st)
	}
	for _, want := range []string{"Sep 6", "podman", "Sep 21", "restarted"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message should mention %q, got %q", want, msg)
		}
	}
}

// Restarted after the upgrade, which is the state every healthy host is in.
func TestServiceStaleQuietWhenCurrent(t *testing.T) {
	installed := time.Date(2026, 9, 21, 18, 28, 0, 0, time.Local)
	started := installed.Add(2 * time.Hour)
	if st, _ := serviceStale(started, installed, "podman"); st != OK {
		t.Errorf("status = %v, want OK", st)
	}
	// Nothing to compare against is not a problem either.
	if st, _ := serviceStale(started, time.Time{}, ""); st != OK {
		t.Errorf("status = %v with no package time, want OK", st)
	}
}
