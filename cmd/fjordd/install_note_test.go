package main

import (
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/manifest"
)

func TestSecretsNoteLeavesTheProviderSecretsOut(t *testing.T) {
	m := &manifest.Manifest{Names: &manifest.NamesProvider{Key: "${RP_PAIRING_KEY}", SetupToken: "${RP_SETUP_TOKEN}"}}
	note := secretsNote(m, "/data/rp", nil, []string{"RP_PAIRING_KEY", "RP_SETUP_TOKEN"})
	if strings.Contains(note, "Made up") || strings.Contains(note, ".env") {
		t.Fatalf("the provider's own secrets were offered to the person:\n%s", note)
	}
	if !strings.Contains(note, "Finish setup") {
		t.Fatalf("no next step:\n%s", note)
	}

	// An app of its own with a password: said as before.
	plain := &manifest.Manifest{}
	note = secretsNote(plain, "/data/db", []string{"DB_PASSWORD"}, []string{"ADMIN_PASSWORD"})
	for _, want := range []string{"kept the passwords", "DB_PASSWORD", "Made up ADMIN_PASSWORD (see the stack's .env)"} {
		if !strings.Contains(note, want) {
			t.Fatalf("missing %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "Finish setup") {
		t.Fatalf("a plain app got the provider's next step:\n%s", note)
	}
}
