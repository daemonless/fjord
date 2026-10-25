package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// One host-networked service gets its web port stashed; two would fight
// over the port, and an app that is not on the host network needs nothing.
func TestSeedHostPortOnlyForTheOneHostedService(t *testing.T) {
	one := "services:\n  actual:\n    image: a\n    network_mode: host\n"
	out, err := seedHostPort(one, "5006", nil)
	if err != nil || !strings.Contains(out, "x-fjord-published") {
		t.Fatalf("one hosted service: %v\n%s", err, out)
	}
	two := one + "  helper:\n    image: b\n    network_mode: host\n"
	if out, _ := seedHostPort(two, "5006", nil); strings.Contains(out, "x-fjord-published") {
		t.Errorf("two hosted services were seeded:\n%s", out)
	}
	if out, _ := seedHostPort("services:\n  web:\n    image: a\n    ports:\n      - 8080:80\n", "8080", nil); strings.Contains(out, "x-fjord-published") {
		t.Errorf("a bridged app was seeded:\n%s", out)
	}
	if out, _ := seedHostPort(one, "", nil); strings.Contains(out, "x-fjord-published") {
		t.Errorf("no web port, yet seeded:\n%s", out)
	}
}

func TestRequestOriginIsWhatTheBrowserUsed(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/apps/install", nil)
	r.Host = "netlab:3567"
	if got := requestOrigin(r); got != "http://netlab:3567" {
		t.Errorf("plain: %q", got)
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := requestOrigin(r); got != "https://netlab:3567" {
		t.Errorf("behind https: %q", got)
	}
}
