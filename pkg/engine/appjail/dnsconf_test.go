package appjail

import (
	"strings"
	"testing"
)

// Only the AppJail bridges: never every interface, never OpenDNS.
func TestDNSConfBody(t *testing.T) {
	body := dnsConfBody([]string{"ajnet", "ajpriv"})
	for _, want := range []string{"addn-hosts=/var/tmp/appjail-hosts", "local=/appjail/", "domain-needed", "bind-dynamic", "interface=ajnet", "interface=ajpriv"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q:\n%s", want, body)
		}
	}
	for _, never := range []string{"no-resolv", "server=", "listen-address=0.0.0.0"} {
		if strings.Contains(body, never) {
			t.Errorf("has %q:\n%s", never, body)
		}
	}
	// No network yet: loopback, since bind-dynamic alone answers everywhere.
	if empty := dnsConfBody(nil); !strings.Contains(empty, "listen-address=127.0.0.1") || strings.Contains(empty, "interface=") {
		t.Errorf("with no networks:\n%s", empty)
	}
}

func TestListensOn(t *testing.T) {
	const out = `USER   COMMAND    PID   FD PROTO  LOCAL ADDRESS         FOREIGN ADDRESS
nobody dnsmasq    38723  4 udp4   10.0.0.1:53           *:*
root   dnsmasq    88044  5 udp4   10.89.1.1:53          *:*
root   unbound    1200   3 udp4   127.0.0.1:53          *:*`
	if !listensOn(out, "10.0.0.1") || listensOn(out, "10.77.0.1") || listensOn(out, "127.0.0.1") {
		t.Fatal("misread which addresses dnsmasq answers on")
	}
}
