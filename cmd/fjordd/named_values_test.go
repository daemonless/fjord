package main

import (
	"testing"

	"github.com/daemonless/fjord/pkg/manifest"
)

// With a name, every public_url is that name and behind_proxy values are
// set; without one, a domain-only URL is left to be typed and the rest get
// this host's address.
func TestFillNamedValues(t *testing.T) {
	yes := "true"
	vars := []manifest.Var{
		{Name: "APP_URL", Type: "public_url", NeedsDomain: true},
		{Name: "MAIL_URL", Type: "public_url"},
		{Name: "TRUST_PROXY", Type: "bool", Default: "false", BehindProxy: &yes},
	}
	host := func() string { return "http://nas:1411" }

	v := map[string]string{"TRUST_PROXY": "false"}
	fillNamedValues(vars, v, "https://pocket-id.apps.lan", host)
	if v["APP_URL"] != "https://pocket-id.apps.lan" || v["MAIL_URL"] != "https://pocket-id.apps.lan" || v["TRUST_PROXY"] != "true" {
		t.Fatalf("named: %v", v)
	}

	v = map[string]string{"TRUST_PROXY": "false"}
	fillNamedValues(vars, v, "", host)
	if v["APP_URL"] != "" || v["MAIL_URL"] != "http://nas:1411" || v["TRUST_PROXY"] != "false" {
		t.Fatalf("not named: %v", v)
	}
}
