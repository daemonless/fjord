package main

import (
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/doctor"
)

func TestConditionsFromChecks(t *testing.T) {
	t0 := time.Date(2026, 10, 8, 5, 29, 0, 0, time.UTC)
	t1 := t0.Add(10 * time.Minute)

	checks := []doctor.Result{
		{ID: "podman", Name: "podman CLI", Engine: "podman", Status: doctor.OK},
		{ID: "epair", Name: "epair", Engine: "podman", Group: "networking", Optional: true, Status: doctor.Warn},
		{ID: "podman-stale", Name: "podman API service is current", Engine: "podman", Status: doctor.Warn, Detail: "running since Oct 1", Action: "Restart podman's API service", Installable: true},
		{ID: "socket", Name: "podman API socket", Engine: "podman", Status: doctor.Fail, Detail: "not answering"},
		{ID: "hostname-taken", Name: "host name resolves to this machine", Group: "networking", Status: doctor.Warn, Detail: "host.lan resolves to 1.2.3.4"},
	}

	conds, since := conditionsFromChecks(checks, map[string]time.Time{"podman-stale": t0}, t1)
	if len(conds) != 3 {
		t.Fatalf("got %d conditions, want 3 (ok and optional excluded): %+v", len(conds), conds)
	}
	// Critical sorts before warning.
	if conds[0].ID != "socket" || conds[0].Severity != "critical" || conds[0].Scope != "engine" {
		t.Errorf("first condition = %+v, want critical socket in engine scope", conds[0])
	}
	if conds[1].ID != "hostname-taken" || conds[1].Scope != "network" {
		t.Errorf("second condition = %+v, want hostname-taken in network scope", conds[1])
	}
	if conds[2].ID != "podman-stale" || conds[2].Since != t0.Format(time.RFC3339) {
		t.Errorf("third condition = %+v, want podman-stale keeping first-seen %s", conds[2], t0.Format(time.RFC3339))
	}
	if !since["socket"].Equal(t1) {
		t.Errorf("new condition since = %v, want %v", since["socket"], t1)
	}

	// Clearing a condition drops its first-seen timestamp.
	conds2, since2 := conditionsFromChecks([]doctor.Result{
		{ID: "socket", Status: doctor.OK},
	}, since, t1.Add(time.Minute))
	if len(conds2) != 0 || len(since2) != 0 {
		t.Errorf("cleared conditions = %+v, since = %+v; want empty", conds2, since2)
	}
}

func TestConditionsChanged(t *testing.T) {
	a := []Condition{{ID: "podman-stale", Severity: "warning", Detail: "old"}}
	b := []Condition{{ID: "podman-stale", Severity: "warning", Detail: "old"}}
	c := []Condition{{ID: "podman-stale", Severity: "warning", Detail: "newer"}}
	if conditionsChanged(a, b) {
		t.Error("identical conditions reported changed")
	}
	if !conditionsChanged(a, c) {
		t.Error("detail change not reported")
	}
	if !conditionsChanged(a, nil) {
		t.Error("clearing conditions not reported")
	}
}
