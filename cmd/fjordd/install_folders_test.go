package main

import (
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/stack"
)

// An install may not put its folders inside another stack's own directory;
// its own directory, folders elsewhere, and the first copy are all fine.
func TestAnotherStacksFolder(t *testing.T) {
	existing := []*stack.Stack{
		{Name: "librenms", State: &stack.State{DisplayName: "librenms"}},
		{Name: "a1b2", State: &stack.State{DisplayName: "Photo Box"}}, // slug photo-box
	}
	dirs := func(ps ...string) []manifest.ProvisionDir {
		var out []manifest.ProvisionDir
		for _, p := range ps {
			out = append(out, manifest.ProvisionDir{Path: p})
		}
		return out
	}
	if why := anotherStacksFolder(dirs("/containers/librenms/config", "/containers/t-lnms/mariadb"), "/containers", "t-lnms", existing); !strings.Contains(why, "/containers/librenms/config is inside librenms's folder") {
		t.Errorf("another stack's folder: %q", why)
	}
	if why := anotherStacksFolder(dirs("/containers/photo-box/data"), "/containers", "t-lnms", existing); !strings.Contains(why, "Photo Box's folder") {
		t.Errorf("by display name slug: %q", why)
	}
	for _, ok := range [][]string{
		{"/containers/t-lnms/config"},   // its own
		{"/mnt/photos"},                 // not under base: the user's
		{"/containers/librenms-2/data"}, // a different folder that merely starts the same
	} {
		if why := anotherStacksFolder(dirs(ok...), "/containers", "t-lnms", existing); why != "" {
			t.Errorf("%v should be allowed, got %q", ok, why)
		}
	}
	// The first copy, or a re-install under the same name, is the owner.
	if why := anotherStacksFolder(dirs("/containers/librenms/config"), "/containers", "librenms", existing); why != "" {
		t.Errorf("own name: %q", why)
	}
}
