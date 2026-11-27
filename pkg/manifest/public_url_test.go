package manifest

import (
	"strings"
	"testing"
)

const pocket = `services:
  app:
    image: x
    environment:
      - APP_URL=${APP_URL}
      - TRUST_PROXY=${TRUST_PROXY}
x-fjord:
  info:
    id: app
  variables:
    - name: APP_URL
      type: public_url
      needs: domain
    - name: TRUST_PROXY
      type: bool
      default: "false"
      behind_proxy: "true"
`

// An app that takes only an https:// domain is refused anything else at
// install, as it would refuse it at start; the two new fields are read.
func TestNeedsDomain(t *testing.T) {
	m, err := Parse(pocket)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Variables[0].NeedsDomain || m.Variables[1].BehindProxy == nil || *m.Variables[1].BehindProxy != "true" {
		t.Fatalf("fields not read: %+v", m.Variables)
	}
	for val, want := range map[string]string{
		"https://auth.example.com":     "",
		"https://auth.apps.lan:8443/x": "",
		"http://auth.example.com":      "needs https://",
		"https://192.0.2.5:1411":       "needs a domain name",
		"https://nas:1411":             "needs a full domain name",
	} {
		_, err := m.Resolve(map[string]string{"APP_URL": val}, "app", "/tmp")
		switch {
		case want == "" && err != nil:
			t.Errorf("%s refused: %v", val, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: got %v, want %q", val, err, want)
		}
	}
}

// Without the field, any http(s) address still does.
func TestAnyAddressWithoutNeeds(t *testing.T) {
	m, err := Parse(strings.Replace(pocket, "      needs: domain\n", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve(map[string]string{"APP_URL": "http://192.0.2.5:1411"}, "app", "/tmp"); err != nil {
		t.Fatal(err)
	}
}
