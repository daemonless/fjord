package main

import (
	"reflect"
	"testing"

	composepkg "github.com/daemonless/fjord/pkg/compose"
)

func TestApplyPathLists(t *testing.T) {
	values := map[string]string{"MOVIES_PATH": "/containers/radarr/movies", "TZ": "UTC"}
	paths := map[string][]string{
		"MOVIES_PATH":    {" /mnt/home/alice ", "", "/mnt/home"},
		"DOWNLOADS_PATH": {"/downloads"},
		"EMPTY_PATH":     {"", "  "},
	}
	exp := applyPathLists(values, paths)

	want := map[string]string{"MOVIES_PATH": "/mnt/home/alice", "DOWNLOADS_PATH": "/downloads", "TZ": "UTC"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("values = %v, want %v", values, want)
	}
	if len(exp) != 1 || exp[0].Var != "MOVIES_PATH" || !reflect.DeepEqual(exp[0].Hosts, []string{"/mnt/home/alice", "/mnt/home"}) {
		t.Fatalf("expansions = %+v", exp)
	}
	if _, ok := values["EMPTY_PATH"]; ok {
		t.Fatal("an all-blank list must not set a value")
	}
}

func TestChooseAppData(t *testing.T) {
	locs := []string{"/ssd/apps", "/hdd/apps"}
	if got := chooseAppData("/hdd/apps", locs); got != "/hdd/apps" {
		t.Fatalf("configured choice: got %q", got)
	}
	if got := chooseAppData("/etc", locs); got != "/ssd/apps" {
		t.Fatalf("unknown path must fall back to the default: got %q", got)
	}
	if got := chooseAppData("", locs); got != "/ssd/apps" {
		t.Fatalf("empty must be the default: got %q", got)
	}
}

func TestExpandPathTemplates(t *testing.T) {
	values := map[string]string{
		"MOVIES_PATH": "{{ appdata }}/{{stack}}/movies",
		"DATA_PATH":   "/mnt/pool/{{stack}}",
		"TZ":          "{{stack}}", // not a path var: untouched
	}
	exps := []pathExpansion{{Var: "MOVIES_PATH", Hosts: []string{"{{base}}/{{stack}}/movies", "/mnt/media/movies"}}} // {{base}} = legacy alias
	dirs := expandPathTemplates(values, exps, map[string]bool{"MOVIES_PATH": true, "DATA_PATH": true}, "radarr-4k", "/containers")

	if values["MOVIES_PATH"] != "/containers/radarr-4k/movies" || values["DATA_PATH"] != "/mnt/pool/radarr-4k" || values["TZ"] != "{{stack}}" {
		t.Fatalf("values = %v", values)
	}
	if !reflect.DeepEqual(exps[0].Hosts, []string{"/containers/radarr-4k/movies", "/mnt/media/movies"}) {
		t.Fatalf("hosts = %v", exps[0].Hosts)
	}
	// Only the path under the storage base is this stack's to create, once.
	if len(dirs) != 1 || dirs[0].Path != "/containers/radarr-4k/movies" || dirs[0].Uid != 1000 {
		t.Fatalf("dirs = %+v", dirs)
	}
}

func TestUniqueSubfolders(t *testing.T) {
	got := uniqueSubfolders([][]string{
		subfolderCandidates("/mnt/home/alice"),
		subfolderCandidates("mars/mnt/sea/alice"),
		subfolderCandidates("/x/alice"),
		subfolderCandidates("/y/alice"),
		subfolderCandidates("/mnt/home"),
	})
	want := []string{"alice", "sea-alice", "x-alice", "y-alice", "home"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	got = uniqueSubfolders([][]string{{"a"}, {"a"}, {"a"}})
	if !reflect.DeepEqual(got, []string{"a", "a-2", "a-3"}) {
		t.Fatalf("numeric fallback: %v", got)
	}
}

// The wizard sends its chosen network AND the per-service rows. The rows win:
// when the chosen one was a built-in, reading Network first put every service
// on the host's stack, threw the plan away, and reported success.
func TestInstallRequestNetworkAction(t *testing.T) {
	perSvc := []composepkg.Attachment{
		{Network: "lan", Service: "immich-server"},
		{Network: "private", Service: "database"},
	}
	for name, tc := range map[string]struct {
		req  installRequest
		want string
	}{
		"nothing asked":         {installRequest{}, netActionNothing},
		"stack-wide none":       {installRequest{Network: "none"}, netActionNone},
		"stack-wide host":       {installRequest{Network: "host"}, netActionHost},
		"stack-wide network":    {installRequest{Network: "lan"}, netActionAttach},
		"per-service rows":      {installRequest{Networks: perSvc}, netActionAttach},
		"modes with no rows":    {installRequest{NetworkModes: map[string]string{"web": "host"}}, netActionAttach},
		"rows beat host":        {installRequest{Network: "host", Networks: perSvc}, netActionAttach},
		"rows beat none":        {installRequest{Network: "none", Networks: perSvc}, netActionAttach},
		"rows beat a named net": {installRequest{Network: "lan", Networks: perSvc}, netActionAttach},
	} {
		if got := tc.req.networkAction(); got != tc.want {
			t.Errorf("%s: networkAction() = %q, want %q", name, got, tc.want)
		}
	}
}

// An install that lists its interfaces per service has already had the app's
// declaration applied -- the wizard resolved it on screen and the operator
// edited it. Re-applying it here dropped the service whose spec is "default":
// that spec matches a stack-wide network, and there is none, so immich-server
// kept `network_mode: host` while the rest of the stack moved to the private
// segment. The install then failed pre-flight on a port it could not bind.
func TestInstallNetworkPlan(t *testing.T) {
	declared := map[string]string{"immich-server": "default", "*": "private"}
	rows := []composepkg.Attachment{{Network: "private", Service: "immich-server"}}

	if got := installNetworkPlan(installRequest{}, declared); len(got) != 2 {
		t.Errorf("no answer in the request: got %v, want the app's own declaration", got)
	}
	if got := installNetworkPlan(installRequest{NetworkPlan: map[string]string{"web": "lan"}}, declared); got["web"] != "lan" {
		t.Errorf("an explicit plan must win over the manifest's: got %v", got)
	}
	if got := installNetworkPlan(installRequest{Networks: rows}, declared); got != nil {
		t.Errorf("a per-service list must apply no plan at all: got %v", got)
	}
	// Both: the list is the later, more specific answer.
	if got := installNetworkPlan(installRequest{Networks: rows, NetworkPlan: declared}, declared); got != nil {
		t.Errorf("a per-service list must win over a plan too: got %v", got)
	}
}
