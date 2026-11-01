package manifest

import (
	"regexp"
	"strings"
	"testing"
)

// woodpecker installed with WOODPECKER_AGENT_SECRET=change-me-to-a-long-random-string:
// the catalog's placeholder was the secret. A required secret left empty is
// made up; one typed, kept from old data, optional or given is left alone.
func TestMakeUpSecrets(t *testing.T) {
	m := &Manifest{Variables: []Var{
		{Name: "AGENT_SECRET", Type: "secret", Default: "change-me"},
		{Name: "TYPED", Type: "secret"},
		{Name: "ADMIN_PASSWORD", Type: "secret", Optional: true},
		{Name: "TUNNEL_TOKEN", Type: "secret", Given: true},
		{Name: "TZ", Type: "string"},
	}}
	values := map[string]string{"TYPED": "mine"}
	made := m.MakeUpSecrets(values)
	if strings.Join(made, ",") != "AGENT_SECRET" {
		t.Errorf("made up %v, want [AGENT_SECRET]", made)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(values["AGENT_SECRET"]) {
		t.Errorf("AGENT_SECRET = %q, want 64 hex characters", values["AGENT_SECRET"])
	}
	for k, want := range map[string]string{"TYPED": "mine", "ADMIN_PASSWORD": "", "TUNNEL_TOKEN": "", "TZ": ""} {
		if values[k] != want {
			t.Errorf("%s = %q, want %q", k, values[k], want)
		}
	}
	other := map[string]string{}
	m.MakeUpSecrets(other)
	if other["AGENT_SECRET"] == values["AGENT_SECRET"] {
		t.Error("two installs got the same secret")
	}
}

// `generate: false` in the manifest marks a secret only its owner has.
func TestParseGenerateFalse(t *testing.T) {
	m, err := Parse(`services:
  app:
    image: x
x-fjord:
  variables:
    - name: TUNNEL_TOKEN
      type: secret
      generate: false
    - name: KEY
      type: secret
`)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Variables[0].Given || m.Variables[0].MadeUp() {
		t.Errorf("TUNNEL_TOKEN: Given=%v MadeUp=%v, want given, not made up", m.Variables[0].Given, m.Variables[0].MadeUp())
	}
	if m.Variables[1].Given || !m.Variables[1].MadeUp() {
		t.Errorf("KEY: Given=%v MadeUp=%v, want made up", m.Variables[1].Given, m.Variables[1].MadeUp())
	}
}

// cloudflared installed with TUNNEL_TOKEN=YOUR_CLOUDFLARE_TOKEN_HERE and
// crash-looped: a given secret left empty is refused, never the placeholder.
func TestResolveGivenSecretNeverDefault(t *testing.T) {
	m := &Manifest{Variables: []Var{
		{Name: "TUNNEL_TOKEN", Type: "secret", Default: "YOUR_CLOUDFLARE_TOKEN_HERE", Given: true},
	}}
	if _, err := m.Resolve(map[string]string{}, "x", "/root"); err == nil || !strings.Contains(err.Error(), "TUNNEL_TOKEN") {
		t.Fatalf("empty given secret: err %v, want a refusal naming it", err)
	}
	res, err := m.Resolve(map[string]string{"TUNNEL_TOKEN": "eyJh"}, "x", "/root")
	if err != nil || res.Env["TUNNEL_TOKEN"] != "eyJh" {
		t.Fatalf("typed token: %v %q", err, res.Env["TUNNEL_TOKEN"])
	}
	m.Variables[0].Optional = true
	if res, err := m.Resolve(map[string]string{}, "x", "/root"); err != nil || res.Env["TUNNEL_TOKEN"] != "" {
		t.Fatalf("optional given secret: %v %q, want empty", err, res.Env["TUNNEL_TOKEN"])
	}
}

// The login's password is one a person can type; every other secret stays
// 64 hex characters.
func TestLoginSecretIsTypeable(t *testing.T) {
	m, err := Parse(`services:
  app:
    image: x
x-fjord:
  info:
    id: app
  login:
    user: admin
    password: ${PASSWORD}
  variables:
    - name: PASSWORD
      type: secret
    - name: KEY
      type: secret
`)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	m.MakeUpSecrets(values)
	if p := values["PASSWORD"]; len(p) != 20 || strings.Trim(p, loginChars) != "" {
		t.Errorf("login password %q", p)
	}
	if k := values["KEY"]; len(k) != 64 {
		t.Errorf("other secret %q", k)
	}
}
