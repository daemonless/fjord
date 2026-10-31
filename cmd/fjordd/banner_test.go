package main

import (
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/manifest"
)

// Only daemonless services read the banner variable; one that sets it keeps
// its own value; the rest of the compose is left alone.
func TestBannerComposeDaemonlessServicesOnly(t *testing.T) {
	compose := `services:
  app:
    image: ghcr.io/daemonless/radarr:${TAG:-latest}
    environment:
      - PUID=1000
  quiet:
    image: ghcr.io/daemonless/postgres:17
    environment:
      DAEMONLESS_BANNER: off
  bare:
    image: ghcr.io/daemonless/redis:7
  other:
    image: docker.io/library/postgres:17
`
	out, err := bannerCompose(compose, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- PUID=1000\n      - DAEMONLESS_BANNER=${DAEMONLESS_BANNER}\n",
		"DAEMONLESS_BANNER: off\n",
		"  bare:\n    image: ghcr.io/daemonless/redis:7\n    environment:\n      - DAEMONLESS_BANNER=${DAEMONLESS_BANNER}\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "${DAEMONLESS_BANNER}") != 2 {
		t.Errorf("want the variable on app and bare only:\n%s", out)
	}
	if !strings.Contains(out, "  other:\n    image: docker.io/library/postgres:17\n") || strings.Contains(out[strings.Index(out, "other:"):], "BANNER") {
		t.Errorf("a foreign image was touched:\n%s", out)
	}
}

// A compose with no daemonless service is returned as it came, byte for byte.
func TestBannerComposeLeavesForeignStacksAlone(t *testing.T) {
	compose := "services:\n  db:\n    image:   docker.io/library/postgres:17   # odd spacing survives\n"
	out, err := bannerCompose(compose, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != compose {
		t.Errorf("rewritten:\n%s", out)
	}
}

// The Makejail's image counts for a service without its own from; an entry
// the bundle wrote stands; a sidecar from another image is left alone.
func TestSetDirectorBanner(t *testing.T) {
	b := &manifest.AppjailBundle{
		Makejail: "ARG tag=latest\nOPTION from=ghcr.io/daemonless/vikunja:${tag}\n",
		Extras:   map[string]string{"caddy.Makejail": "OPTION from=docker.io/library/caddy:2\n"},
		Director: `services:
  vikunja:
    name: vikunja
    options:
      - expose: !ENV '${WEB_PORT}:3456 proto:tcp'
    oci:
      environment:
        - TZ: !ENV '${TZ}'
  db:
    name: db
    options:
      - from: ghcr.io/daemonless/postgres:17
  mono:
    name: mono
    options:
      - from: ghcr.io/daemonless/redis:7
    oci:
      environment:
        - DAEMONLESS_BANNER: mono
  caddy:
    name: caddy
    options:
      - makejail: !ENV '${PWD}/caddy.Makejail'
  plain:
    name: plain
    options:
      - from: docker.io/library/nginx:1
`,
	}
	out, err := setDirectorBanner(b.Director, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- TZ: !ENV '${TZ}'\n        - DAEMONLESS_BANNER: !ENV '${DAEMONLESS_BANNER}'\n",
		"  db:\n    name: db\n    options:\n      - from: ghcr.io/daemonless/postgres:17\n    oci:\n      environment:\n        - DAEMONLESS_BANNER: !ENV '${DAEMONLESS_BANNER}'\n",
		"- DAEMONLESS_BANNER: mono\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "${DAEMONLESS_BANNER}") != 2 {
		t.Errorf("want the variable on vikunja and db only:\n%s", out)
	}
	for _, svc := range []string{"caddy:", "plain:"} {
		tail := out[strings.Index(out, svc):]
		if i := strings.Index(tail, "\n  "); i > 0 && strings.Contains(tail[:i], "BANNER") {
			t.Errorf("%s got the variable:\n%s", svc, out)
		}
	}
	if strings.Contains(out, `'!ENV`) {
		t.Errorf("!ENV became a quoted string:\n%s", out)
	}
	// Nothing to add: the text is returned untouched.
	same, err := setDirectorBanner(out, b)
	if err != nil || same != out {
		t.Errorf("a second pass changed the director (%v)", err)
	}
}

// .env: color by default, the bundle's own value kept, the wizard's over it.
func TestDirectorEnvBanner(t *testing.T) {
	cases := []struct{ defaults, resolved, want string }{
		{"WEB_PORT=1\n", "", "DAEMONLESS_BANNER=color\n"},
		{"DAEMONLESS_BANNER=mono\n", "", "DAEMONLESS_BANNER=mono\n"},
		{"DAEMONLESS_BANNER=mono\n", "off", "DAEMONLESS_BANNER=off\n"},
		{"", "plain", "DAEMONLESS_BANNER=plain\n"},
	}
	for _, c := range cases {
		resolved := map[string]string{}
		if c.resolved != "" {
			resolved["DAEMONLESS_BANNER"] = c.resolved
		}
		env := directorEnv(c.defaults, "s", resolved, nil, nil, nil)
		if !strings.Contains(env, c.want) || strings.Count(env, "DAEMONLESS_BANNER=") != 1 {
			t.Errorf("defaults %q resolved %q: want %q in:\n%s", c.defaults, c.resolved, c.want, env)
		}
	}
}
