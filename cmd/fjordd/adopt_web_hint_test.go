package main

import "testing"

// The catalog's web port is only taken for the catalog app's own image:
// another image of the same app can answer on another port.
func TestRunsImage(t *testing.T) {
	lsPlex := "services:\n  plex:\n    image: lscr.io/linuxserver/plex:latest\n"
	cases := []struct {
		compose, image string
		want           bool
	}{
		{lsPlex, "docker.io/plexinc/pms-docker:plexpass", false},
		{lsPlex, "lscr.io/linuxserver/plex:1.43", true},
		{lsPlex, "lscr.io/linuxserver/plex@sha256:abc", true},
		{"services:\n  web:\n    image: nginx:1.27\n", "docker.io/library/nginx:latest", true},
		{"services:\n  web:\n    image: ghcr.io/daemonless/radarr:latest\n", "ghcr.io/daemonless/sonarr:latest", false},
	}
	for _, c := range cases {
		if got := runsImage(c.compose, c.image); got != c.want {
			t.Errorf("runsImage(%q, %q) = %v, want %v", c.compose, c.image, got, c.want)
		}
	}
}

// An adopted app's web port is written as the host port it is published
// on, which is what the Open link reads; unpublished, it stays as it is.
func TestPublishedFor(t *testing.T) {
	compose := "services:\n  web:\n    image: nginx\n    ports:\n      - \"18181:80\"\n      - \"127.0.0.1:9443:443/tcp\"\n      - \"${DNS}:53/udp\"\n  other:\n    image: x\n    ports:\n      - \"7000:3000\"\n"
	cases := []struct{ service, port, want string }{
		{"web", "80", "18181"},
		{"web", "443", "9443"},
		{"web", "53", "53"},     // udp only: not a web port
		{"web", "3000", "3000"}, // another service's port
		{"", "3000", "7000"},    // no service named: any service
		{"web", "8080", "8080"}, // not published
	}
	for _, c := range cases {
		if got := publishedFor(compose, "DNS=5353\n", c.service, c.port); got != c.want {
			t.Errorf("publishedFor(%s, %s) = %s, want %s", c.service, c.port, got, c.want)
		}
	}
}
