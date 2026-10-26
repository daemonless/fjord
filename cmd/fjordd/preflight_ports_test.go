package main

import (
	"context"
	"testing"

	"github.com/daemonless/fjord/pkg/stack"
)

// Another stack set to run claims its ports from its config, with no live
// status needed: zensical-2 started on vikunja's 3456 before the status
// arrived. A stopped stack, or one with an address of its own, claims none.
func TestOtherStackPorts(t *testing.T) {
	s := &server{manager: stack.NewManager(t.TempDir())}
	add := func(name, compose, desired string) {
		t.Helper()
		if err := s.manager.Save(&stack.Stack{Name: name, Compose: compose, Env: "WEB_PORT=3456\n"}); err != nil {
			t.Fatal(err)
		}
		if err := s.manager.SaveState(name, &stack.State{DesiredState: desired}); err != nil {
			t.Fatal(err)
		}
	}
	add("vikunja", "services:\n  vikunja:\n    image: v\n    ports:\n      - \"${WEB_PORT}:3456\"\n", "running")
	add("old", "services:\n  old:\n    image: o\n    ports:\n      - \"8080:80\"\n", "stopped")
	got := s.otherStackPorts(&stack.Stack{Name: "zensical-2"})
	if got["3456/tcp"] != "vikunja" || len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	if mine := s.otherStackPorts(&stack.Stack{Name: "vikunja"}); len(mine) != 0 {
		t.Fatalf("its own ports: %v", mine)
	}
}

// A host-network app's page port comes from its x-fjord web_port hint,
// resolved through .env, before its image is pulled: plausible (host
// network, 8000) installed over zensical's published 8000 and crash-looped,
// because EXPOSE was all pre-flight read and the image was not there yet.
func TestHostNetworkPortsFromWebPortHint(t *testing.T) {
	s := &server{manager: stack.NewManager(t.TempDir())}
	st := &stack.Stack{Name: "plausible", Env: "WEB_PORT=8000\n",
		Compose: "services:\n  plausible:\n    image: p\n    network_mode: host\n  postgres:\n    image: pg\n    network_mode: host\nx-fjord:\n  web_port: \"${WEB_PORT}\"\n"}
	got := s.hostNetworkPorts(context.Background(), st, st.EnvMap())
	if len(got) != 1 || got[0].key != "8000/tcp" || got[0].service != "plausible" || got[0].container != "plausible_plausible_1" {
		t.Fatalf("got %+v", got)
	}
	bridged := &stack.Stack{Name: "zensical", Compose: "services:\n  zensical:\n    image: z\n    ports:\n      - \"8000:8000\"\nx-fjord:\n  web_port: \"8000\"\n"}
	if got := s.hostNetworkPorts(context.Background(), bridged, nil); len(got) != 0 {
		t.Fatalf("a published port is not a host-network one: %+v", got)
	}
}
