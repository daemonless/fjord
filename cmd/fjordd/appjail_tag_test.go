package main

import (
	"context"
	"strings"
	"testing"

	composepkg "github.com/daemonless/fjord/pkg/compose"
)

// zensical's shape: one service, no `from:` of its own, so its image is the
// Makejail's `<image>:${tag}`. Installing 0.0.59 wrote 0.0.59 into the compose,
// ran latest, and offered an upgrade that pressing changed nothing.
const tagDirector = `services:
  zensical:
    name: zensical_zensical
    options:
      - container: 'args:--pull'
      - bridge: 'epair:zensical bridge:lanbridge'
    volumes:
      - ZENSICAL_CONFIG_PATH: /config
`

func TestSetDirectorTag(t *testing.T) {
	out, err := setDirectorTag(tagDirector, "0.0.59")
	if err != nil {
		t.Fatalf("setDirectorTag: %v", err)
	}
	if !strings.Contains(out, "arguments:") || !strings.Contains(out, "- tag: '0.0.59'") {
		t.Fatalf("tag not recorded:\n%s", out)
	}
	// Nothing else is disturbed.
	for _, keep := range []string{"zensical_zensical", "args:--pull", "ZENSICAL_CONFIG_PATH"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
	// Setting it again replaces rather than appends: a stack upgraded twice
	// would otherwise carry every version it has ever been.
	out2, err := setDirectorTag(out, "0.0.62")
	if err != nil {
		t.Fatalf("second setDirectorTag: %v", err)
	}
	if strings.Contains(out2, "0.0.59") {
		t.Errorf("old tag survived:\n%s", out2)
	}
	if n := strings.Count(out2, "- tag:"); n != 1 {
		t.Errorf("got %d tag arguments, want 1:\n%s", n, out2)
	}
}

// A service naming its own image does not use the Makejail's ${tag}, so
// stamping an app's version on it would say something untrue.
func TestSetDirectorTagSkipsServicesWithOwnImage(t *testing.T) {
	out, err := setDirectorTag(servicesDirector, "9.9.9")
	if err != nil {
		t.Fatalf("setDirectorTag: %v", err)
	}
	if strings.Contains(out, "9.9.9") {
		t.Errorf("tagged a service that names its own image:\n%s", out)
	}
	// Unchanged means unchanged -- not reserialized.
	if out != servicesDirector {
		t.Errorf("document was rewritten for nothing")
	}
}

func TestComposeImageTag(t *testing.T) {
	cases := map[string]string{
		"services:\n  a:\n    image: ghcr.io/daemonless/zensical:0.0.59\n":    "0.0.59",
		"services:\n  a:\n    image: ghcr.io/daemonless/zensical\n":           "",
		"services:\n  a:\n    image: registry:5000/app\n":                     "",
		"services:\n  a:\n    image: ghcr.io/x/y:1.2\n  b:\n    image: r:3\n": "",
	}
	for compose, want := range cases {
		if got := composeImageTag(compose); got != want {
			t.Errorf("composeImageTag(%q) = %q, want %q", compose, got, want)
		}
	}
}

// A mode written only into the compose is invisible to appjail, so "host" on
// one service of a director stack did nothing at all.
func TestSetDirectorModes(t *testing.T) {
	out, err := setDirectorModes(servicesDirector, map[string]string{
		"immich-server": "host",
		"redis":         "bridge",
	})
	if err != nil {
		t.Fatalf("setDirectorModes: %v", err)
	}
	// host is alias + inherit, which is how a jail shares the host's stack.
	for _, want := range []string{"- alias:", "- ip4_inherit:", "- ip6_inherit:"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// bridge is appjail's own NAT network.
	if !strings.Contains(out, "virtualnet: ':<random> default'") {
		t.Errorf("bridge did not become appjail's NAT network:\n%s", out)
	}
	// Everything the bundle put there survives.
	for _, keep := range []string{"ghcr.io/daemonless/immich-postgres:latest", "immich_database"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
}

// A host-stack jail needs the ip4 its template carries, so it must go back to
// the base template rather than the .net variant with ip4 stripped out.
func TestSetDirectorModesRestoresTemplate(t *testing.T) {
	seedNetwork(t)
	attached, err := setDirectorNetworks(context.Background(), templateDirector, "immich",
		[]composepkg.Attachment{{Network: "vlan6", Service: "database"}})
	if err != nil {
		t.Fatalf("setDirectorNetworks: %v", err)
	}
	if !strings.Contains(attached, "immich-postgres-template.net.conf") {
		t.Fatalf("setup wrong -- expected the networked variant:\n%s", attached)
	}
	out, err := setDirectorModes(attached, map[string]string{"database": "host"})
	if err != nil {
		t.Fatalf("setDirectorModes: %v", err)
	}
	if strings.Contains(out, ".net.conf") {
		t.Errorf("a host-stack jail kept the variant with ip4 removed:\n%s", out)
	}
}

func TestSetDirectorModesUnknownService(t *testing.T) {
	if _, err := setDirectorModes(servicesDirector, map[string]string{"postgres": "host"}); err == nil {
		t.Fatal("accepted a mode for a service the director does not have")
	}
}
