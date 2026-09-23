package main

import (
	"reflect"
	"testing"
)

// Director builds a service from its own makejail:, falling back to the
// project's "Makejail" (director/default.py). Only those services need one.
func TestServicesWithoutMakejail(t *testing.T) {
	spec := `services:
  documentserver:
    makejail: gh+AppJail-makejails/documentserver
  local:
    makejail: ./Makejail
  bare:
    name: bare
`
	if got, want := servicesWithoutMakejail(spec), []string{"bare", "local"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := servicesWithoutMakejail("services:\n  ds:\n    makejail: gh+AppJail-makejails/documentserver\n"); len(got) != 0 {
		t.Errorf("every service names its own: got %v, want none", got)
	}
}
