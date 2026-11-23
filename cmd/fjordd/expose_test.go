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

// A service that comes back to appjail's own network from the host's stack
// publishes its ports again; one still on the host's stack, one whose ports
// are set aside and one that kept its expose line are left alone.
func TestRestoreExpose(t *testing.T) {
	director := `services:
  web:
    name: web
    options:
      - container: 'args:--pull'
      - virtualnet: ':<random> default'
      - nat:
  onhost:
    name: onhost
    options:
      - alias:
      - ip4_inherit:
  kept:
    name: kept
    options:
      - virtualnet: ':<random> default'
      - nat:
      - expose: '9000:9000 proto:tcp'
  parked:
    name: parked
    options:
      - virtualnet: ':<random> default'
      - nat:
`
	compose := `services:
  web:
    ports:
      - "${WEB_PORT}:3000"
      - "5353:53/udp"
      - "127.0.0.1:8080:80"
  onhost:
    ports:
      - "7000:7000"
  kept:
    ports:
      - "9000:9000"
  parked:
    x-fjord-published:
      - "6000:6000"
`
	out, err := restoreExpose(director, compose)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"expose: !ENV '${WEB_PORT}:3000 proto:tcp'", "expose: '5353:53 proto:udp'", "args:--pull"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	for _, not := range []string{"7000", "8080", "6000"} {
		if strings.Contains(out, not) {
			t.Errorf("%s should not be exposed:\n%s", not, out)
		}
	}
	if strings.Count(out, "9000:9000") != 1 {
		t.Errorf("kept's expose was doubled or lost:\n%s", out)
	}
	if again, _ := restoreExpose(out, compose); again != out {
		t.Errorf("a second pass changed it:\n%s", again)
	}
	if same, _ := restoreExpose(director, "services:\n  web:\n    image: x\n"); same != director {
		t.Errorf("nothing to publish, yet it changed:\n%s", same)
	}
}
