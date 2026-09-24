package main

import (
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/updates"
)

// A refresh runs for minutes. A stack updated meanwhile must not get the
// refresh's older "behind" back (notes kept "1 update available" after its
// update); one it never touched takes the refresh's result.
func TestFleetSwapKeepsNewerEntries(t *testing.T) {
	var f fleetUpdates
	f.put("old", updates.Status{State: "available"})
	started := time.Now()
	time.Sleep(time.Millisecond)
	f.forget("updated")                                // an update finished mid-refresh
	f.put("checked", updates.Status{State: "current"}) // the stack page checked mid-refresh

	f.swap(map[string]updates.Status{
		"old":     {State: "current"},
		"updated": {State: "available"},
		"checked": {State: "available"},
	}, started)

	if _, ok := f.results["updated"]; ok {
		t.Errorf("updated got the refresh's stale verdict back: %+v", f.results["updated"])
	}
	if got := f.results["checked"].State; got != "current" {
		t.Errorf("checked = %s, want the newer current", got)
	}
	if got := f.results["old"].State; got != "current" {
		t.Errorf("old = %s, want the refresh's current", got)
	}
}
