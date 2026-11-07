package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// appjailAllowlist is the shrinking set of non-test cmd/fjordd files that may
// still name "appjail" directly. New features must dispatch through
// pkg/engine.Backend or Capabilities() rather than adding engine-name branches
// in HTTP handlers.
var appjailAllowlist = map[string]bool{
	"conditions_handler.go": true,
	"install_handler.go":    true,
	"install_networking.go": true,
	"install_plan.go":       true,
	"stack_handlers.go":     true,
}

func TestEngineBoundaryAllowlist(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"appjail"`) && !appjailAllowlist[name] {
			t.Errorf("%s contains literal \"appjail\"; dispatch through pkg/engine.Backend instead", name)
		}
	}
}
