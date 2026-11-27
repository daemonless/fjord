package main

import (
	"strings"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/updates"
)

func at(hm string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-23 "+hm, time.Local)
	return t
}

func TestWindowAt(t *testing.T) {
	cases := []struct {
		now, start, end string
		inside          bool
		opened          string // "-1 23:00" is the day before
	}{
		{"03:30", "03:00", "05:00", true, "03:00"},
		{"02:59", "03:00", "05:00", false, "03:00"},
		{"05:00", "03:00", "05:00", false, "+1 03:00"},
		{"23:30", "23:00", "01:00", true, "23:00"},
		{"00:30", "23:00", "01:00", true, "-1 23:00"},
		{"12:00", "23:00", "01:00", false, "23:00"},
	}
	for _, c := range cases {
		inside, opened := windowAt(at(c.now), c.start, c.end)
		want := at(strings.TrimPrefix(strings.TrimPrefix(c.opened, "-1 "), "+1 "))
		if strings.HasPrefix(c.opened, "-1 ") {
			want = want.AddDate(0, 0, -1)
		}
		if strings.HasPrefix(c.opened, "+1 ") {
			want = want.AddDate(0, 0, 1)
		}
		if inside != c.inside || !opened.Equal(want) {
			t.Errorf("%s in %s-%s: got %v %v, want %v %v", c.now, c.start, c.end, inside, opened, c.inside, want)
		}
	}
	if _, ok := parseHM("3:00"); ok {
		t.Error("3:00 taken")
	}
	if _, ok := parseHM("24:00"); ok {
		t.Error("24:00 taken")
	}
}

func TestPlanAuto(t *testing.T) {
	now := at("05:00")
	day := 24 * time.Hour
	svc := func(name string, p updates.Policy, c updates.Class, seen time.Duration) autoService {
		return autoService{Name: name, Policy: p, Cand: updates.Candidate{Class: c, FirstSeen: now.Add(-seen)}}
	}
	on := updates.Minors
	ok := autoStack{Name: "app", PerService: true}

	// Off for every service: not listed at all.
	st := ok
	st.Services = []autoService{svc("web", updates.Off, updates.Rebuild, day)}
	if a := planAuto(st, now); a.Result != "" || len(a.Services) != 0 {
		t.Fatalf("off: %+v", a)
	}

	// On: a rebuild and a minor go, a major and an unknown wait.
	st.Services = []autoService{
		svc("web", on, updates.Rebuild, 0),
		svc("api", on, updates.Minor, 25*time.Hour),
		svc("ml", on, updates.Major, 25*time.Hour),
		svc("x", on, updates.Unknown, 25*time.Hour),
	}
	a := planAuto(st, now)
	if strings.Join(a.Services, ",") != "web,api" || a.Result != "" {
		t.Fatalf("on: %+v", a)
	}

	// A version first seen at 03:10 last night is due when this window
	// closes at 05:00 (25h50m), not the night after.
	st.Services = []autoService{svc("api", on, updates.Minor, 25*time.Hour+50*time.Minute)}
	if a := planAuto(st, now); len(a.Services) != 1 {
		t.Fatalf("soaked at close: %+v", a)
	}
	st.Services = []autoService{svc("api", on, updates.Minor, 2*time.Hour)}
	if a := planAuto(st, now); a.Result != "waiting" || !strings.Contains(a.Reason, "soaking") {
		t.Fatalf("soaking: %+v", a)
	}

	// A database others depend on takes rebuilds only.
	db := svc("db", on, updates.Minor, 48*time.Hour)
	db.Cand.Dependency = true
	st.Services = []autoService{db}
	if a := planAuto(st, now); a.Result != "waiting" {
		t.Fatalf("dependency: %+v", a)
	}

	// A version bump is retagged first.
	bump := svc("web", on, updates.Patch, 48*time.Hour)
	bump.NewTag = "2.1.4"
	st.Services = []autoService{bump}
	if a := planAuto(st, now); len(a.Retags) != 1 || a.Retags[0].tag != "2.1.4" {
		t.Fatalf("retag: %+v", a)
	}

	// Skipped, never forced.
	for _, c := range []struct {
		mod  func(*autoStack)
		want string
	}{
		{func(s *autoStack) { s.Stopped = true }, "stopped"},
		{func(s *autoStack) { s.Pending = true }, "saved changes"},
		{func(s *autoStack) { s.PerService = false }, "engine"},
	} {
		s := autoStack{Name: "app", PerService: true, Services: []autoService{svc("web", on, updates.Rebuild, 0)}}
		c.mod(&s)
		if a := planAuto(s, now); a.Result != "skipped" || !strings.Contains(a.Reason, c.want) {
			t.Errorf("%s: %+v", c.want, a)
		}
	}
}
