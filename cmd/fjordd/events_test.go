package main

import (
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
)

// A port change is a state change for the names sync: the key the event
// loop compares carries the published ports.
func TestPublishedPortsChangeTheKey(t *testing.T) {
	st := engine.StackStatus{Containers: []engine.ContainerStatus{{Name: "a", Ports: []engine.Port{{HostPort: 18081, ContainerPort: 80}}}}}
	if got := publishedPorts(st); got != "18081:80" {
		t.Fatalf("ports: %q", got)
	}
	st.Containers[0].Ports[0].HostPort = 18082
	if got := publishedPorts(st); got != "18082:80" {
		t.Fatalf("ports after change: %q", got)
	}
	if got := publishedPorts(engine.StackStatus{}); got != "" {
		t.Fatalf("no containers: %q", got)
	}
}
