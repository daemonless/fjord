package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/daemonless/fjord/pkg/stack"
)

func TestStackNames(t *testing.T) {
	hosts := []providerHost{
		{Name: "sabnzbd", URL: "https://sabnzbd.home.example.com/", Ref: nameRef("sabnzbd"), ExtraNames: []string{"sab"}},
		{Name: "radarr", URL: "https://radarr.home.example.com/", Ref: nameRef("radarr")},
		{Name: "sabnzbd", URL: "https://sabnzbd.home.example.com/", Ref: "made by hand"},
	}
	got := stackNames(hosts, "sabnzbd")
	want := []string{"sabnzbd.home.example.com", "sab.home.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := stackNames(hosts, "lidarr"); len(got) != 0 {
		t.Fatalf("no name: got %v", got)
	}
}

func TestFillNameEnv(t *testing.T) {
	hints := map[string]string{"HOST_ALLOWLIST": "{names}", "BASE_URL": "https://{name}"}
	got := fillNameEnv(hints, []string{"a.home.lan", "b.home.lan"}, nil)
	if got["HOST_ALLOWLIST"] != "a.home.lan,b.home.lan" || got["BASE_URL"] != "https://a.home.lan" {
		t.Fatalf("got %v", got)
	}
	// Names taken away: the variables empty, not left at the old names.
	if got := fillNameEnv(hints, nil, nil); got["HOST_ALLOWLIST"] != "" || got["BASE_URL"] != "https://" {
		t.Fatalf("no names: got %v", got)
	}
}

func TestFillNameEnvProxy(t *testing.T) {
	hints := map[string]string{"TRUSTED_PROXIES": "{proxy}"}
	if !wantsProxy(hints) || wantsProxy(map[string]string{"HOST_ALLOWLIST": "{names}"}) {
		t.Fatal("wantsProxy")
	}
	got := fillNameEnv(hints, []string{"ha.home.lan"}, []string{"10.89.0.0/24", "10.89.1.0/24"})
	if got["TRUSTED_PROXIES"] != "10.89.0.0/24,10.89.1.0/24" {
		t.Fatalf("got %v", got)
	}
}

// A linked provider's requests come from its host.
func TestProxySourcesLinked(t *testing.T) {
	s := &server{}
	got := s.proxySources(context.Background(), &provider{linked: true, base: "https://192.168.4.20:8443"})
	if !reflect.DeepEqual(got, []string{"192.168.4.20"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSubnetOf(t *testing.T) {
	subs := []string{"", "192.168.86.0/24", "10.89.0.0/24"}
	if got := subnetOf("10.89.0.6", subs); got != "10.89.0.0/24" {
		t.Errorf("bridge address: %q", got)
	}
	if got := subnetOf("192.168.5.16", subs); got != "192.168.5.16" {
		t.Errorf("no subnet known: %q", got)
	}
}

func TestSetEnvValues(t *testing.T) {
	env := "PUID=1000\n# a comment\nHOST_ALLOWLIST=old.lan\n"
	got, changed := setEnvValues(env, map[string]string{"HOST_ALLOWLIST": "new.lan", "BASE_URL": "https://new.lan"})
	want := "PUID=1000\n# a comment\nHOST_ALLOWLIST=new.lan\nBASE_URL=https://new.lan\n"
	if !changed || got != want {
		t.Fatalf("got %q", got)
	}
	if _, changed := setEnvValues(want, map[string]string{"HOST_ALLOWLIST": "new.lan"}); changed {
		t.Fatal("same value: nothing changed")
	}
	if got, _ := setEnvValues("", map[string]string{"A": "1"}); got != "A=1\n" {
		t.Fatalf("empty env: got %q", got)
	}
}

func TestNameEnvHintsFromCompose(t *testing.T) {
	s := &server{}
	st := &stack.Stack{Compose: "services:\n  app:\n    image: x\nx-fjord:\n  web_port: \"8080\"\n  name_env:\n    HOST_ALLOWLIST: \"{names}\"\n    \"bad key\": \"{name}\"\n"}
	got := s.nameEnvHints(context.Background(), st)
	if !reflect.DeepEqual(got, map[string]string{"HOST_ALLOWLIST": "{names}"}) {
		t.Fatalf("got %v", got)
	}
	if got := s.nameEnvHints(context.Background(), &stack.Stack{Compose: "services: {}\n"}); got != nil {
		t.Fatalf("no hint, no catalog: got %v", got)
	}
}
