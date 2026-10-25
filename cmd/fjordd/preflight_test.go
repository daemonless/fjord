package main

import "testing"

// A running host-network service owns its ports whatever its container is
// called: container_name: pkg-cache is not pkg-cache_pkg-cache_1.
func TestOwnsHostPortBySeviceWhenTheNameDiffers(t *testing.T) {
	hp := hostPort{key: "80/tcp", port: 80, proto: "tcp", service: "pkg-cache", container: "pkg-cache_pkg-cache_1"}
	if !ownsHostPort(hp, map[string]bool{"pkg-cache": true}, map[string]bool{"pkg-cache": true}) {
		t.Fatal("a running service with its own container name should own its port")
	}
	if !ownsHostPort(hp, map[string]bool{"pkg-cache_pkg-cache_1": true}, nil) {
		t.Fatal("the engine's default name should still match")
	}
	if ownsHostPort(hp, map[string]bool{"pkg-cache": false}, map[string]bool{"pkg-cache": false}) {
		t.Fatal("a stopped service holds nothing")
	}
}
