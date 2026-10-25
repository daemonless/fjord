package main

import (
	"os"
	"strings"
	"testing"
)

// The pasted link carries the proxy's address and the code; an address
// with the code typed beside it works too; junk is refused with a hint.
func TestParseLink(t *testing.T) {
	base, code, err := parseLink("https://proxy.apps.lan/link#code=hxfy-4v5y-4zrv-uv43", "")
	if err != nil || base != "https://proxy.apps.lan" || code != "HXFY-4V5Y-4ZRV-UV43" {
		t.Fatalf("link: %q %q %v", base, code, err)
	}
	base, code, err = parseLink("192.168.4.103:8443", "ABCD-EFGH")
	if err != nil || base != "https://192.168.4.103:8443" || code != "ABCD-EFGH" {
		t.Fatalf("address + code: %q %q %v", base, code, err)
	}
	if _, _, err := parseLink("https://proxy.apps.lan/", ""); err == nil || !strings.Contains(err.Error(), "no code") {
		t.Fatalf("no code: %v", err)
	}
	if _, _, err := parseLink("", ""); err == nil {
		t.Fatal("empty accepted")
	}
}

// Refs carry this fjord's id; the ones written before the id existed carry
// the host's name, and both are this fjord's.
func TestOwnKeyTakesTheIdAndTheOldHostForm(t *testing.T) {
	instanceID = "k7f3a9b2c1d0"
	if nameRef("zensical") != "fjord:k7f3a9b2c1d0:zensical" {
		t.Fatalf("ref: %s", nameRef("zensical"))
	}
	if k, ok := ownKey("fjord:k7f3a9b2c1d0:crafting-apps/photocraft"); !ok || k != "crafting-apps/photocraft" {
		t.Fatalf("own by id: %q %v", k, ok)
	}
	host, _ := os.Hostname()
	if k, ok := ownKey("fjord:" + strings.SplitN(host, ".", 2)[0] + ":zensical"); !ok || k != "zensical" {
		t.Fatalf("own by host: %q %v", k, ok)
	}
	if _, ok := ownKey("fjord:someone-else:zensical"); ok {
		t.Fatal("another fjord's ref taken as ours")
	}
}

// The id is made once and kept; the name starts as the host's.
func TestEnsureInstanceIsStable(t *testing.T) {
	root := t.TempDir()
	a := ensureInstance(root)
	b := ensureInstance(root)
	if a.ID == "" || a.ID != b.ID || a.Name == "" || a.Label() != "fjord on "+a.Name {
		t.Fatalf("instance: %+v %+v", a, b)
	}
}
