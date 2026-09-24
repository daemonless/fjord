package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stack writes one stack's director spec into a stacks dir.
func stack(t *testing.T, dir, name, spec string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, name), 0o755)
	if err := os.WriteFile(filepath.Join(dir, name, "appjail-director.yml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
}

// With no tools on PATH, the checks warn only for what a stack actually uses.
func TestAppjailToolChecks(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	stacks := t.TempDir()
	ctx := context.Background()

	if st, _ := appjailGitProbe(stacks)(ctx); st != OK {
		t.Errorf("git missing, no stack needs it: %s, want ok", st)
	}
	if st, _ := appjailSecretsProbe(stacks)(ctx); st != OK {
		t.Errorf("rage missing, no stack needs it: %s, want ok", st)
	}

	stack(t, stacks, "documentserver", "services:\n  ds:\n    makejail: gh+AppJail-makejails/documentserver\n    options:\n      - secret: documentserver\n")
	if st, msg := appjailGitProbe(stacks)(ctx); st != Warn || !strings.Contains(msg, "documentserver") {
		t.Errorf("git: %s %q, want a warning naming documentserver", st, msg)
	}
	if st, msg := appjailSecretsProbe(stacks)(ctx); st != Warn || strings.Contains(msg, "video player") {
		t.Errorf("secrets: %s %q, want a warning (no video-player note: no rage installed)", st, msg)
	}

	// `pkg install rage` puts the EFL video player at bin/rage.
	os.WriteFile(filepath.Join(bin, "rage"), []byte("#!/bin/sh\n"), 0o755)
	if _, msg := appjailSecretsProbe(stacks)(ctx); !strings.Contains(msg, "video player") {
		t.Errorf("secrets with the wrong rage: %q, want the video-player note", msg)
	}
}
