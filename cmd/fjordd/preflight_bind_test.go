package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/stack"
)

// A file mounted on its own is not a folder to make: the stack starts. A
// missing folder is still made.
func TestProvisionBindDirsFile(t *testing.T) {
	root := t.TempDir()
	conf := filepath.Join(root, "app", "launcher")
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "app", "data")
	compose := "services:\n  app:\n    image: x\n    volumes:\n" +
		"      - " + conf + ":/daemonless/libexec/app:ro\n" +
		"      - " + data + ":/config\n"
	s := &server{fjordRoot: root}
	if problems := s.provisionBindDirs(&stack.Stack{Name: "app", Compose: compose}, map[string]string{"PUID": strconv.Itoa(os.Getuid()), "PGID": strconv.Itoa(os.Getgid())}); len(problems) > 0 {
		t.Fatalf("problems: %s", strings.Join(problems, "; "))
	}
	if fi, err := os.Stat(data); err != nil || !fi.IsDir() {
		t.Fatalf("data folder not made: %v", err)
	}
	if fi, err := os.Stat(conf); err != nil || fi.IsDir() {
		t.Fatalf("the file changed: %v", err)
	}
}
