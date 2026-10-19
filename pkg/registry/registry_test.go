package registry

import "testing"

func TestManifestRef(t *testing.T) {
	for _, c := range []struct{ image, host, repo, ref string }{
		{"ghcr.io/daemonless/radarr:latest", "ghcr.io", "daemonless/radarr", "latest"},
		{"ghcr.io/daemonless/radarr", "ghcr.io", "daemonless/radarr", "latest"},
		{"ghcr.io/daemonless/radarr:5.1@sha256:abcd", "ghcr.io", "daemonless/radarr", "sha256:abcd"},
		{"ghcr.io/daemonless/radarr@sha256:abcd", "ghcr.io", "daemonless/radarr", "sha256:abcd"},
		{"registry.lan:5000/app:2", "registry.lan:5000", "app", "2"},
	} {
		h, r, ref := manifestRef(c.image)
		if h != c.host || r != c.repo || ref != c.ref {
			t.Errorf("%s: got %s %s %s, want %s %s %s", c.image, h, r, ref, c.host, c.repo, c.ref)
		}
	}
}
