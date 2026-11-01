package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
)

// A compose project made outside fjord is imported from its own files: the
// compose with its folder's paths made absolute, and its .env.
func TestImportSpec(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  app:\n    image: a\n    volumes:\n      - ./data:/data\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("A=1\n"), 0o644)
	a, err := importSpec(engine.Unmanaged{Project: "app", WorkDir: dir, ComposeFiles: []string{"compose.yaml"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Compose, dir+"/data:/data") || a.Env != "A=1\n" || a.Service != "app" {
		t.Errorf("spec: %+v", a)
	}
	if _, err := importSpec(engine.Unmanaged{Project: "gone", WorkDir: dir, ComposeFiles: []string{"missing.yaml"}}); err == nil {
		t.Error("a missing compose file imported")
	}
}
