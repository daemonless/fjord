package main

import (
	"slices"
	"testing"

	"github.com/daemonless/fjord/pkg/catalog"
)

func ids(s []catalog.Source) []string {
	var out []string
	for _, c := range s {
		out = append(out, c.ID)
	}
	return out
}

func TestNewDefaults(t *testing.T) {
	linux := defaultCatalogs("linux")
	ls := linux[:1]
	cases := []struct {
		name         string
		sources      []catalog.Source
		given        []string
		bootstrapped bool
		want, wantG  []string
	}{
		{"fresh Linux gets both", nil, nil, false, []string{"linuxserver", "daemonless"}, []string{"linuxserver", "daemonless"}},
		// venus: bootstrapped with linuxserver before daemonless was a default
		{"0.4.0 adds daemonless once", ls, nil, true, []string{"linuxserver", "daemonless"}, []string{"linuxserver", "daemonless"}},
		{"removed stays removed", ls, []string{"linuxserver", "daemonless"}, true, []string{"linuxserver"}, []string{"linuxserver", "daemonless"}},
		{"removed main default stays removed", nil, nil, true, []string{"daemonless"}, []string{"linuxserver", "daemonless"}},
		{"added by hand is not doubled", []catalog.Source{ls[0], {ID: "mine", URL: "https://catalog.daemonless.io/v1/daemonless/"}}, nil, true, []string{"linuxserver", "mine"}, []string{"linuxserver", "daemonless"}},
	}
	for _, c := range cases {
		got, given := newDefaults(c.sources, c.given, c.bootstrapped, linux)
		if !slices.Equal(ids(got), c.want) || !slices.Equal(given, c.wantG) {
			t.Errorf("%s: got %v given %v, want %v given %v", c.name, ids(got), given, c.want, c.wantG)
		}
	}
	if got, _ := newDefaults(nil, nil, false, defaultCatalogs("freebsd")); !slices.Equal(ids(got), []string{"daemonless"}) {
		t.Errorf("FreeBSD: %v", ids(got))
	}
}
