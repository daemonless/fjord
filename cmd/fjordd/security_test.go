package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/catalog"
)

func TestValidSourceURL(t *testing.T) {
	ok := []string{
		"https://catalog.daemonless.io",
		"http://127.0.0.1:8099",
		"http://[::1]:8099",
		"http://localhost:8099",
		"http://192.168.1.50:8080/catalog",
		"http://mybox.lan:8080",
	}
	for _, u := range ok {
		if err := catalog.ValidSourceURL(u); err != nil {
			t.Errorf("ValidSourceURL(%q) = %v, want nil", u, err)
		}
	}
	bad := []string{
		"http://example.com/catalog",
		"http://8.8.8.8/catalog",
		"ftp://example.com/catalog",
		"javascript:alert(1)",
	}
	for _, u := range bad {
		if err := catalog.ValidSourceURL(u); err == nil {
			t.Errorf("ValidSourceURL(%q) = nil, want error", u)
		}
	}
}

func TestSetEnvValuesStripsControlCharacters(t *testing.T) {
	out, changed := setEnvValues("FOO=bar\n", map[string]string{
		"HOST_ALLOWLIST": "radarr.lan\nEVIL=injected",
		"BAD\nKEY":       "val",
		"GOOD_HOST":      "radarr.lan",
	})
	if !changed {
		t.Fatal("expected GOOD_HOST to be written")
	}
	if strings.Contains(out, "EVIL=injected") || strings.Contains(out, "BAD") {
		t.Fatalf("control character injection succeeded: %q", out)
	}
	if !strings.Contains(out, "GOOD_HOST=radarr.lan") {
		t.Fatalf("expected GOOD_HOST in output: %q", out)
	}
}

func TestLoginLimiterIPv6PrefixAndInFlight(t *testing.T) {
	k1 := limitKey("2001:db8:abcd:1234::1")
	k2 := limitKey("2001:db8:abcd:1234:ffff:ffff:ffff:ffff")
	if k1 != k2 {
		t.Fatalf("expected same /64 bucket: %q vs %q", k1, k2)
	}
	lim := newSigninLimit()
	rel, busy := lim.acquire("192.0.2.1")
	if busy || rel == nil {
		t.Fatal("first acquire should succeed")
	}
	if _, busy2 := lim.acquire("192.0.2.1"); !busy2 {
		t.Fatal("concurrent acquire on same bucket should be rejected while in flight")
	}
	rel()
	rel3, busy3 := lim.acquire("192.0.2.1")
	if busy3 || rel3 == nil {
		t.Fatal("acquire after release should succeed")
	}
	rel3()
}

func TestNamesLinkKeyMigratesToSecrets(t *testing.T) {
	root := t.TempDir()
	st := savedSettings{
		NamesLink: &NamesLink{
			URL:       "https://proxy.lan:8443",
			Key:       "secret-pairing-key",
			ProxyName: "proxy.lan",
		},
	}
	if err := saveSettings(root, st); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(settingsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings.json perm = %o, want 0600", fi.Mode().Perm())
	}
	loaded := loadSettings(root)
	got := loadNamesLinkKey(root, loaded.NamesLink)
	if got != "secret-pairing-key" {
		t.Fatalf("loadNamesLinkKey = %q, want secret-pairing-key", got)
	}
	raw, err := os.ReadFile(settingsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-pairing-key") {
		t.Fatalf("settings.json still contains plaintext key after migration: %s", raw)
	}
}

func TestRedactSecretsAndURLCredentials(t *testing.T) {
	in := "PUID=1000\nDB_PWD=supersecret\nCUSTOM_VAR=myval\nDATABASE_URL=postgres://user:hunter2@db:5432/app\n"
	out := redactSecrets(in, map[string]bool{"CUSTOM_VAR": true})
	if strings.Contains(out, "supersecret") || strings.Contains(out, "myval") || strings.Contains(out, "hunter2") {
		t.Fatalf("redactSecrets leaked secret: %q", out)
	}
	if !strings.Contains(out, "DATABASE_URL=postgres://user:@db:5432/app") {
		t.Fatalf("expected redacted DATABASE_URL, got: %q", out)
	}
}

func TestLinkedProviderRefusesUnverifiedLook(t *testing.T) {
	ctx := context.Background()
	f := newFakeProxy(t, "k")
	f.complete = true
	pinPath := filepath.Join(t.TempDir(), "secrets", "names-linked.json")
	p := &provider{Stack: "rp", base: f.srv.URL, key: "k", pinPath: pinPath, linked: true}
	if _, _, err := p.status(ctx); err != nil || p.pin == nil {
		t.Fatalf("initial pin: %v", err)
	}

	// Present a different certificate pretending to be a reset or renamed proxy.
	// Because p.linked is true, fjord must NOT make an unverified request with p.key.
	other := newFakeProxy(t, "k", selfSigned(t, "evil.test"))
	other.complete = false
	q := &provider{Stack: "rp", base: other.srv.URL, key: "k", pinPath: pinPath, pin: loadPin(pinPath), linked: true}
	if _, _, err := q.status(ctx); err == nil {
		t.Fatal("linked provider accepted untrusted certificate")
	}
	if loadPin(pinPath) == nil {
		t.Fatal("linked provider dropped pin on untrusted certificate")
	}
}
