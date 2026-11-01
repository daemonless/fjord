package main

import (
	"strings"
	"testing"
)

// Vikunja on MariaDB: the password changed in .env after the first start --
// and then put back, which must not be warned about (it is the fix).
func TestSecretChangeWarnings(t *testing.T) {
	secrets := map[string]bool{"VIKUNJA_DATABASE_PASSWORD": true}
	setUp := map[string]string{"VIKUNJA_DATABASE_PASSWORD": "first", "TZ": "UTC"}
	changed := map[string]string{"VIKUNJA_DATABASE_PASSWORD": "second", "TZ": "Europe/Paris"}

	// A stack from before sums were kept: the value before the Save is the baseline.
	base := secretBaseline(nil, setUp, secrets)
	warns := secretChangeWarnings(base, changed, secrets, true)
	if len(warns) != 1 || !strings.HasPrefix(warns[0], "VIKUNJA_DATABASE_PASSWORD changed") {
		t.Fatalf("changed: %q", warns)
	}
	// Put back: the baseline kept, so no warning.
	if w := secretChangeWarnings(secretBaseline(base, changed, secrets), setUp, secrets, true); w != nil {
		t.Errorf("put back is the fix, not a change: %q", w)
	}
	if strings.Contains(base["VIKUNJA_DATABASE_PASSWORD"], "first") {
		t.Error("the secret itself is stored, not its sum")
	}
	if w := secretChangeWarnings(base, changed, secrets, false); w != nil {
		t.Errorf("never run, nothing to disagree with: %q", w)
	}
	if w := secretChangeWarnings(base, changed, nil, true); w != nil {
		t.Errorf("no declared secrets, nothing guessed: %q", w)
	}
}
