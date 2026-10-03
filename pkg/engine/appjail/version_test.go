package appjail

import "testing"

// AppJail installed while fjordd runs is seen without a restart; once found,
// the version is not asked for again.
func TestVersionIsNotStuckOnNotInstalled(t *testing.T) {
	old := probe
	t.Cleanup(func() { probe = old; versionVal = "" })
	versionVal = ""

	calls, installed := 0, false
	probe = func() string {
		calls++
		if installed {
			return "5.7.0"
		}
		return ""
	}
	if v := Version(); v != "" {
		t.Fatalf("not installed: got %q", v)
	}
	installed = true // Setup's Install button
	if v := Version(); v != "5.7.0" {
		t.Fatalf("after install: got %q, want 5.7.0", v)
	}
	before := calls
	Version()
	if calls != before {
		t.Errorf("a found version was probed again")
	}
}
