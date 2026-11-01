package main

import (
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/stack"
)

// Paths are cleaned before the prefix checks (review 1.3): ".." lexically
// under App data is not the app's to provision, and a folder that climbs
// into another stack's is that stack's.
func TestPathChecksClean(t *testing.T) {
	dirs := expandPathTemplates(map[string]string{"DATA": "/containers/app/../../etc/foo"}, nil, map[string]bool{"DATA": true}, "app", "/containers")
	for _, d := range dirs {
		if strings.Contains(d.Path, "etc") {
			t.Errorf("provisioned %s", d.Path)
		}
	}
	other := []*stack.Stack{{Name: "other"}}
	msg := anotherStacksFolder([]manifest.ProvisionDir{{Path: "/containers/app/../other/data"}}, "/containers", "app", other)
	if !strings.Contains(msg, "other") {
		t.Errorf("climbing into another stack's folder passed: %q", msg)
	}
}

func TestDatasetOverrideRefusesDotDot(t *testing.T) {
	m, err := manifest.Parse("services:\n  a:\n    image: x\n    volumes:\n      - ${DATA}:/data\nx-fjord:\n  info: {id: a, name: a}\n  variables:\n    - {name: DATA, type: zfs_dataset, default: data}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve(map[string]string{"DATA": "/containers/a/../../etc"}, "a", "/containers"); err == nil {
		t.Error("an override climbing out with .. was accepted")
	}
}
