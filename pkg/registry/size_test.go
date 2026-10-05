package registry

import "testing"

func TestImageRef(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/daemonless/immich-ml":            "latest",
		"ghcr.io/daemonless/immich-ml:15-pkg":     "15-pkg",
		"localhost:5000/app":                      "latest",
		"localhost:5000/app:1.2":                  "1.2",
		"ghcr.io/daemonless/redis@sha256:abc0123": "sha256:abc0123",
	} {
		if got := imageRef(in); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}
