package updates

import (
	"strings"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC)
	day := now.Add(-25 * time.Hour) // seen long enough ago to have soaked
	for _, tc := range []struct {
		name   string
		p      Policy
		c      Candidate
		act    bool
		reason string
	}{
		{"off", Off, Candidate{Class: Rebuild}, false, "off"},
		{"unset is off", "", Candidate{Class: Rebuild}, false, "off"},
		{"notify", Notify, Candidate{Class: Rebuild}, false, "notify"},
		{"rebuilds takes a rebuild at once", Rebuilds, Candidate{Class: Rebuild, FirstSeen: now}, true, "rebuild"},
		{"rebuilds refuses a patch", Rebuilds, Candidate{Class: Patch, FirstSeen: day}, false, "does not take a patch"},
		{"patch takes a soaked patch", Patches, Candidate{Class: Patch, FirstSeen: day}, true, "patch"},
		{"patch waits out the soak", Patches, Candidate{Class: Patch, FirstSeen: now.Add(-6 * time.Hour)}, false, "soaking: 19h left"},
		{"minor refuses a major", Minors, Candidate{Class: Major, FirstSeen: day}, false, "does not take a major"},
		{"unknown needs all", Minors, Candidate{Class: Unknown, FirstSeen: day}, false, "unknown size"},
		{"all takes unknown", All, Candidate{Class: Unknown, FirstSeen: day}, true, "unknown"},
		{"pinned waits", All, Candidate{Class: Rebuild, Pinned: true}, false, "pinned"},
		{"database takes a rebuild", All, Candidate{Class: Rebuild, Dependency: true}, true, "rebuild"},
		{"database refuses a minor", All, Candidate{Class: Minor, FirstSeen: day, Dependency: true}, false, "only takes rebuilds"},
		{"never seen soaks", All, Candidate{Class: Minor}, false, "just seen"},
	} {
		v := Decide(tc.p, tc.c, now)
		if v.Act != tc.act || !strings.Contains(v.Reason, tc.reason) {
			t.Errorf("%s: %+v, want act=%v with %q", tc.name, v, tc.act, tc.reason)
		}
	}
}

func TestParsePolicy(t *testing.T) {
	if p, ok := ParsePolicy(""); !ok || p != Off {
		t.Errorf(`"" = %q %v, want off`, p, ok)
	}
	if _, ok := ParsePolicy("sometimes"); ok {
		t.Error("an unknown policy was accepted")
	}
}
