package main

import (
	"reflect"
	"testing"

	"github.com/daemonless/fjord/pkg/engine"
)

// A fresh host has no engine yet: the setup page must still list the checks
// of the engine fjord would use, or it shows nothing to install.
func TestDoctorEnginesOnAHostWithNone(t *testing.T) {
	t.Setenv("FJORD_ENGINE", "")
	s := &server{fjordRoot: t.TempDir(), backends: map[string]engine.Backend{}}
	if got, want := s.doctorEngines(), []string{engineDescriptors[0].Name}; !reflect.DeepEqual(got, want) {
		t.Fatalf("no engines: got %v, want %v", got, want)
	}

	t.Setenv("FJORD_ENGINE", "appjail")
	if got, want := s.doctorEngines(), []string{"appjail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FJORD_ENGINE=appjail: got %v, want %v", got, want)
	}

	t.Setenv("FJORD_ENGINE", "nonsense")
	if got, want := s.doctorEngines(), []string{engineDescriptors[0].Name}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unknown FJORD_ENGINE: got %v, want %v", got, want)
	}
}

// Once an engine is registered, the checks follow what is registered.
func TestDoctorEnginesFollowsRegistered(t *testing.T) {
	s := &server{fjordRoot: t.TempDir(), defEngine: "appjail",
		backends: map[string]engine.Backend{"appjail": engine.Unavailable("appjail")}}
	if got, want := s.doctorEngines(), []string{"appjail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
