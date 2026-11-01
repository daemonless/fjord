package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests never ask the host's DNS: no jail DNS unless a test says so.
func TestMain(m *testing.M) {
	jailDNSAnswers = func(context.Context) bool { return false }
	virtualnetGateway = func(context.Context, string) string { return "" }
	os.Exit(m.Run())
}

func withJailDNS(t *testing.T, gateways map[string]string) {
	t.Helper()
	answers, gw := jailDNSAnswers, virtualnetGateway
	jailDNSAnswers = func(context.Context) bool { return true }
	virtualnetGateway = func(_ context.Context, n string) string { return gateways[n] }
	t.Cleanup(func() { jailDNSAnswers, virtualnetGateway = answers, gw })
}

const twoOnDefault = `options:
  - virtualnet: ':<random> default'
  - nat:
services:
  vikunja:
    name: v
  mariadb:
    name: m
`

func TestDirectorResolvSharedNetwork(t *testing.T) {
	withJailDNS(t, map[string]string{"ajnet": "10.0.0.1"})
	dir := t.TempDir()
	out, err := setDirectorResolv(context.Background(), twoOnDefault, dir)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "resolv.ajnet.conf")
	if n := strings.Count(out, "resolv_conf: "+file); n != 2 {
		t.Fatalf("want both services on %s, got %d:\n%s", file, n, out)
	}
	b, _ := os.ReadFile(file)
	if string(b) != "search ajnet.appjail\nnameserver 10.0.0.1\n" {
		t.Fatalf("resolv: %q", b)
	}
	// Idempotent: a second save does not add a second option.
	again, _ := setDirectorResolv(context.Background(), out, dir)
	if strings.Count(again, "resolv_conf") != 2 {
		t.Fatalf("second pass:\n%s", again)
	}
}

// Nothing answering on :53: jails keep the host's resolver, which still
// resolves outside names.
func TestDirectorResolvNoDNS(t *testing.T) {
	out, err := setDirectorResolv(context.Background(), twoOnDefault, t.TempDir())
	if err != nil || strings.Contains(out, "resolv_conf") {
		t.Fatalf("err %v:\n%s", err, out)
	}
}

// A service alone on its network, or on the host's stack, has no sibling to
// find, and keeps LAN names.
func TestDirectorResolvOnlyShared(t *testing.T) {
	withJailDNS(t, map[string]string{"ajnet": "10.0.0.1", "app_priv": "10.100.0.1"})
	yml := `services:
  app:
    name: a
    options:
      - virtualnet: 'ajnet:e0 default'
  db:
    name: d
    options:
      - virtualnet: 'app_priv:e1 default'
  host:
    name: h
    options:
      - virtualnet: 'app_priv:e2 default'
      - ip4_inherit:
  cache:
    name: c
    options:
      - virtualnet: 'app_priv:e3 default'
      - resolv_conf: /old
`
	out, err := setDirectorResolv(context.Background(), yml, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "resolv.ajnet.conf") || strings.Contains(out, "/old") {
		t.Fatalf("app is alone on ajnet; stale option kept:\n%s", out)
	}
	if n := strings.Count(out, "resolv.app_priv.conf"); n != 2 {
		t.Fatalf("want db and cache on app_priv's DNS, got %d:\n%s", n, out)
	}
}

// A jail on a DHCP LAN and the project's network keeps the jail resolver: the
// lease's DNS would replace it at boot, and immich-server could not find its
// database. A jail with no lease gets nothing extra.
func TestDirectorResolvKeptOverDHCP(t *testing.T) {
	withJailDNS(t, map[string]string{"immich_priv": "10.101.0.1"})
	const yml = `services:
  server:
    name: s
    options:
      - bridge: 'epair:s0 bridge:lanbridge'
      - dhcp: 'sb_s0'
      - virtualnet: 'immich_priv:s1'
  database:
    name: d
    options:
      - virtualnet: 'immich_priv:d0'
`
	dir := t.TempDir()
	out, err := setDirectorResolv(context.Background(), yml, dir)
	if err != nil {
		t.Fatal(err)
	}
	files := filepath.Join(dir, "jailfiles")
	if strings.Count(out, "copydir: "+files) != 1 || strings.Count(out, "file: /etc/resolvconf.conf") != 1 {
		t.Fatalf("want the guard on the DHCP jail only:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(files, "etc", "resolvconf.conf")); string(b) != "resolvconf=NO\n" {
		t.Fatalf("resolvconf.conf: %q", b)
	}
	again, _ := setDirectorResolv(context.Background(), out, dir)
	if strings.Count(again, "copydir:") != 1 || strings.Count(again, "file:") != 1 {
		t.Fatalf("second pass:\n%s", again)
	}
}
