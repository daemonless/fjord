package main

import (
	"strings"
	"testing"
)

// woodpecker's bundle runs on the host's stack (alias, ip4_inherit) and
// carries its ports as expose lines; appjail refused `up` ("expose requires
// the following options: virtualnet") on netlab, 2026-10-11.
func TestDropExposeOffVirtualnet(t *testing.T) {
	host := `options:
  - alias:
  - ip4_inherit:
services:
  web:
    name: web
    options:
      - container: 'args:--pull'
      - expose: '8000:8000 proto:tcp'
  side:
    name: side
    options:
      - virtualnet: 'priv:side1'
      - expose: '9000:9000 proto:tcp'
`
	out, err := dropExposeOffVirtualnet(host)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "8000:8000") {
		t.Errorf("web has no virtualnet and kept its expose:\n%s", out)
	}
	if !strings.Contains(out, "9000:9000") || !strings.Contains(out, "args:--pull") {
		t.Errorf("side's own virtualnet or web's other options were lost:\n%s", out)
	}

	nat := "options:\n  - virtualnet: ':<random> default'\n  - nat:\nservices:\n  web:\n    name: web\n    options:\n      - expose: '8000:8000 proto:tcp'\n"
	if out, _ := dropExposeOffVirtualnet(nat); out != nat {
		t.Errorf("a project on virtualnet was changed:\n%s", out)
	}
}
