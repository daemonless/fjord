package main

import (
	"strings"
	"testing"
)

func TestSelinuxAction(t *testing.T) {
	cases := []struct {
		name, typ, opts string
		ours            bool
		label           bool
		problem         string
	}{
		{"fjord's folder, default label", "default_t", "", true, true, ""},
		{"already a container's", "container_file_t", "", false, false, ""},
		{"podman relabels it (:z)", "default_t", "ro,z", false, false, ""},
		{"podman relabels it (:Z)", "user_home_t", "Z", false, false, ""},
		{"someone else's folder", "samba_share_t", "", false, false, "Add :z"},
		{"no label read", "", "", false, false, ""},
	}
	for _, c := range cases {
		label, problem := selinuxAction("/m", c.typ, c.opts, c.ours)
		if label != c.label || (c.problem == "") != (problem == "") || !strings.Contains(problem, c.problem) {
			t.Errorf("%s: label=%v problem=%q", c.name, label, problem)
		}
	}
}
