package main

import (
	"context"
	"strings"
	"testing"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/stack"
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
		if got := composeImageTag(compose, ""); got != want {
			t.Errorf("composeImageTag(%q) = %q, want %q", compose, got, want)
		}
	}
}

// A choice's database is a second image; the tag is the one of the image the
// Makejail builds from. Missed, the jail ran :pkg under a compose saying
// :latest, and Update never cleared.
func TestComposeImageTagMakejailImage(t *testing.T) {
	compose := "services:\n  vikunja:\n    image: ghcr.io/daemonless/vikunja:latest\n  mariadb:\n    image: ghcr.io/daemonless/mariadb:11.4\n"
	mj := "ARG tag=pkg\n\nOPTION container=boot\nOPTION from=ghcr.io/daemonless/vikunja:${tag}\n"
	if got := composeImageTag(compose, mj); got != "latest" {
		t.Fatalf("got %q", got)
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

// A whole stack on host: every jail shares the host's stack, and nothing of
// the NAT network is left to clash with ip4_inherit -- not the project's
// virtualnet, and not the service's expose, which redirects from a NAT that
// is no longer there.
func TestAllHost(t *testing.T) {
	const two = `options:
  - virtualnet: ':<random> default'
  - nat:
services:
  web:
    name: app_web
    options:
      - expose: '8000:8000 proto:tcp'
      - container: 'args:--pull'
  db:
    name: app_db
`
	modes, err := allHost(two)
	if err != nil {
		t.Fatalf("allHost: %v", err)
	}
	if len(modes) != 2 || modes["web"] != composepkg.Host || modes["db"] != composepkg.Host {
		t.Fatalf("modes = %v, want both services on host", modes)
	}
	out, err := setDirectorModes(two, modes)
	if err != nil {
		t.Fatalf("setDirectorModes: %v", err)
	}
	for _, want := range []string{"- alias:", "- ip4_inherit:", "- ip6_inherit:"} {
		if strings.Count(out, want) != 2 {
			t.Errorf("want %q on both services:\n%s", want, out)
		}
	}
	for _, gone := range []string{"virtualnet", "nat:", "expose"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived on a host stack:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "container: 'args:--pull'") {
		t.Errorf("lost the bundle's own option:\n%s", out)
	}
}

func TestSetDirectorModesUnknownService(t *testing.T) {
	if _, err := setDirectorModes(servicesDirector, map[string]string{"postgres": "host"}); err == nil {
		t.Fatal("accepted a mode for a service the director does not have")
	}
}

// The bundle's project-level virtualnet applies to every jail on top of the
// service's own: a bridge mode written per service met it twice ("ajnet
// cannot be used again", exit 78; zensical on netlab, 2026-09-30). The
// project keeps no networking options; a service given no mode inherits them
// explicitly instead.
func TestSetDirectorModesMovesProjectNetworkToServices(t *testing.T) {
	const two = `options:
  - virtualnet: ':<random> default'
  - nat:
services:
  web:
    name: app_web
    options:
      - expose: '8000:8000 proto:tcp'
  db:
    name: app_db
`
	out, err := setDirectorModes(two, map[string]string{"web": "bridge"})
	if err != nil {
		t.Fatalf("setDirectorModes: %v", err)
	}
	if strings.Count(out, "virtualnet:") != 2 {
		t.Errorf("want exactly one virtualnet per service and none on the project:\n%s", out)
	}
	head := strings.SplitN(out, "services:", 2)[0]
	if strings.Contains(head, "virtualnet") || strings.Contains(head, "nat:") {
		t.Errorf("the project kept networking options:\n%s", out)
	}
	db := strings.SplitN(out, "  db:", 2)[1]
	if !strings.Contains(db, "virtualnet: ':<random> default'") || !strings.Contains(db, "nat:") {
		t.Errorf("the untouched service lost the project's network:\n%s", out)
	}
	// A service that names its own network is not handed the project's.
	own := "options:\n  - virtualnet: ':<random> default'\n  - nat:\nservices:\n  web:\n    name: app_web\n  db:\n    name: app_db\n    options:\n      - bridge: 'epair:eb_db bridge:vlan6'\n"
	out, err = setDirectorModes(own, map[string]string{"web": "bridge"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "virtualnet:") != 1 || !strings.Contains(out, "bridge: 'epair:eb_db bridge:vlan6'") {
		t.Errorf("a service on its own network changed:\n%s", out)
	}
}

const pinDirector = `services:
  vikunja:
    name: vikunja_vikunja
    options:
      - container: 'args:--pull'
  vikunja-mariadb:
    name: vikunja_mariadb
    options:
      - from: ghcr.io/daemonless/mariadb:11.4
      - template: !ENV '${PWD}/template.conf'
`

const pinDigest = "sha256:64a5270f0e2d8e0b1d3c4f8a7b6e5d4c3b2a1908f7e6d5c4b3a291807f6e5d4c"

// A pin reaches the jail: the Makejail service through its tag argument,
// digest and all, a sidecar through its own from:. Nothing else moves.
func TestSetDirectorImage(t *testing.T) {
	out, err := setDirectorImage(pinDirector, "vikunja", "ghcr.io/daemonless/vikunja:latest@"+pinDigest, "ghcr.io/daemonless/vikunja")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "- tag: 'latest@"+pinDigest+"'") {
		t.Fatalf("tag argument not pinned:\n%s", out)
	}
	out, err = setDirectorImage(out, "vikunja-mariadb", "ghcr.io/daemonless/mariadb:11.4@"+pinDigest, "ghcr.io/daemonless/vikunja")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from: ghcr.io/daemonless/mariadb:11.4@"+pinDigest) {
		t.Fatalf("from: not pinned:\n%s", out)
	}
	for _, keep := range []string{"args:--pull", "!ENV '${PWD}/template.conf'", "vikunja_mariadb"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
	// The Makejail builds one repository; another cannot be said there.
	if _, err := setDirectorImage(pinDirector, "vikunja", "docker.io/other/thing:1", "ghcr.io/daemonless/vikunja"); err == nil {
		t.Fatal("an image of another repository was written to the tag argument")
	}
	if _, err := setDirectorImage(pinDirector, "nope", "x:1", ""); err == nil {
		t.Fatal("a service the director lacks was not refused")
	}
}

func TestTagRef(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/daemonless/app:latest":           "latest",
		"ghcr.io/daemonless/app:pkg@" + pinDigest: "pkg@" + pinDigest,
		"ghcr.io/daemonless/app@" + pinDigest:     "latest@" + pinDigest,
		"localhost:5000/app:2.1":                  "2.1",
	} {
		if got := tagRef(in); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

// Rollback and Version... write the compose; the director follows for the
// services named, expanding the compose's variables, and leaves the rest.
func TestDirectorFollowsCompose(t *testing.T) {
	st := &stack.Stack{
		Director: pinDirector,
		Makejail: "OPTION from=ghcr.io/daemonless/vikunja:${tag}\n",
		Compose: "services:\n  vikunja:\n    image: ghcr.io/daemonless/vikunja:${VIKUNJA_TAG:-latest}@" + pinDigest +
			"\n  vikunja-mariadb:\n    image: ghcr.io/daemonless/mariadb:11.8\n",
		Env: "VIKUNJA_TAG=pkg\n",
	}
	if err := directorFollowsCompose(st, []string{"vikunja"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.Director, "- tag: 'pkg@"+pinDigest+"'") {
		t.Fatalf("vikunja not pinned from the compose:\n%s", st.Director)
	}
	if !strings.Contains(st.Director, "from: ghcr.io/daemonless/mariadb:11.4") {
		t.Fatalf("a service not named was changed:\n%s", st.Director)
	}
	if err := directorFollowsCompose(st, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.Director, "from: ghcr.io/daemonless/mariadb:11.8") {
		t.Fatalf("every service follows when none is named:\n%s", st.Director)
	}
	// A podman stack has no director: nothing to do.
	if err := directorFollowsCompose(&stack.Stack{Compose: st.Compose}, nil); err != nil {
		t.Fatal(err)
	}
}

// A director project with no compose (made from a director file): rollback
// and unpin write the director itself.
func TestSetServiceImageDirectorOnly(t *testing.T) {
	st := &stack.Stack{
		Director: pinDirector,
		Makejail: "ARG tag=2.0.5\nOPTION from=ghcr.io/daemonless/vikunja:${tag}\n",
	}
	if got := serviceImage(st, "vikunja"); got != "ghcr.io/daemonless/vikunja:2.0.5" {
		t.Fatalf("vikunja runs %q, want the Makejail's default tag", got)
	}
	if err := setServiceImage(st, "vikunja", "ghcr.io/daemonless/vikunja:2.0.5@"+pinDigest); err != nil {
		t.Fatal(err)
	}
	if err := setServiceImage(st, "vikunja-mariadb", "ghcr.io/daemonless/mariadb:11.4@"+pinDigest); err != nil {
		t.Fatal(err)
	}
	if got := serviceImage(st, "vikunja"); got != "ghcr.io/daemonless/vikunja:2.0.5@"+pinDigest {
		t.Fatalf("vikunja now %q", got)
	}
	if got := serviceImage(st, "vikunja-mariadb"); got != "ghcr.io/daemonless/mariadb:11.4@"+pinDigest {
		t.Fatalf("mariadb now %q", got)
	}
	// Unpinning puts the tag back without the digest.
	if err := setServiceImage(st, "vikunja", withoutDigest(serviceImage(st, "vikunja"))); err != nil {
		t.Fatal(err)
	}
	if got := serviceImage(st, "vikunja"); got != "ghcr.io/daemonless/vikunja:2.0.5" {
		t.Fatalf("after unpin vikunja is %q", got)
	}
	if st.Compose != "" {
		t.Fatalf("a compose appeared: %q", st.Compose)
	}
}
