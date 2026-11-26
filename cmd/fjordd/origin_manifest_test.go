package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/daemonless/fjord/pkg/catalog"
	"github.com/daemonless/fjord/pkg/stack"
)

// Two catalogs carry the same app id: a stack reads the manifest of the one
// it came from, and one installed before the catalog was recorded reads the
// first that has it, as before.
func TestOriginManifestReadsItsCatalog(t *testing.T) {
	root := t.TempDir()
	catDir := filepath.Join(root, "catalog")
	for src, body := range map[string]string{"first": "from first", "second": "from second"} {
		dir := filepath.Join(catDir, src)
		if err := os.MkdirAll(filepath.Join(dir, "manifests"), 0o755); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"apps": []map[string]any{{"id": "app", "name": "App", "manifest_url": "/catalog/" + src + "/manifests/app.yaml"}}})
		if err := os.WriteFile(filepath.Join(dir, "catalog.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifests", "app.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := &server{cat: catalog.New(catDir, []catalog.Source{
		{ID: "first", Name: "first", URL: "http://example.invalid/first"},
		{ID: "second", Name: "second", URL: "http://example.invalid/second"},
	})}
	read := func(src string) string {
		b, err := s.originManifest(context.Background(), &stack.Stack{State: &stack.State{Origin: stack.OriginInfo{Type: "catalog", AppID: "app", Catalog: src}}})
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		return string(b)
	}
	if got := read("second"); got != "from second" {
		t.Errorf("from second: got %q", got)
	}
	if got := read(""); got != "from first" {
		t.Errorf("not recorded: got %q", got)
	}
	if got := read("gone"); got != "from first" {
		t.Errorf("catalog gone: got %q", got)
	}
	if _, err := s.originManifest(context.Background(), &stack.Stack{}); err == nil {
		t.Error("no origin: no error")
	}
}
