package main

import "testing"

// The catalog's web port is only taken for the catalog app's own image:
// linuxserver's plex said 3005, the pms-docker venus runs answers on 32400.
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
