package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	composepkg "github.com/daemonless/fjord/pkg/compose"
)

// immich's shape: four services, one of which needs a jail template for
// PostgreSQL's SysV shared memory -- and that template carries the ip4/ip6
// inherit lines that put a host-networked jail on the host's stack.
const templateDirector = `options:
  - alias:
  - ip4_inherit:
services:
  immich-server:
    name: immich_immich_server
    options:
      - from: ghcr.io/daemonless/immich-server:latest
  redis:
    name: immich_redis
    options:
      - from: ghcr.io/daemonless/redis:latest
  database:
    name: immich_database
    options:
      - from: ghcr.io/daemonless/immich-postgres:latest
      - template: !ENV '${PWD}/immich-postgres-template.conf'
`

const postgresTemplate = `# The jail PostgreSQL runs in.

exec.start: "/bin/sh /etc/rc"
sysvmsg: new
sysvsem: new
sysvshm: new
mount.devfs
persist
ip4: inherit
ip6: inherit
`

func TestStripJailAddressing(t *testing.T) {
	out := stripJailAddressing(postgresTemplate)
	// The reason the template exists survives.
	for _, keep := range []string{"sysvmsg: new", "sysvsem: new", "sysvshm: new", "mount.devfs", "persist", "exec.start"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
	// What jail(8) refuses next to vnet does not.
	for _, gone := range []string{"ip4", "ip6"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived:\n%s", gone, out)
		}
	}
}

// The ip4.addr/ip6.addr family is refused by jail(8) for the same reason and
// is the spelling someone else's template is likelier to use.
func TestStripJailAddressingParamFamily(t *testing.T) {
	in := "ip4.addr = 192.168.4.9;\nip6.addr: ::1\nip4\nallow.raw_sockets\nvip4_thing: keep\n"
	out := stripJailAddressing(in)
	for _, gone := range []string{"ip4.addr", "ip6.addr", "192.168.4.9"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived:\n%s", gone, out)
		}
	}
	if strings.Contains(out, "\nip4\n") {
		t.Errorf("bare ip4 survived:\n%s", out)
	}
	// A parameter that merely starts with the same letters is not one of them.
	for _, keep := range []string{"allow.raw_sockets", "vip4_thing: keep"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
}

func TestNetTemplateName(t *testing.T) {
	for in, want := range map[string]string{
		"immich-postgres-template.conf": "immich-postgres-template.net.conf",
		"template.conf":                 "template.net.conf",
		"plain":                         "plain.net",
		"template.net.conf":             "template.net.conf", // already there
	} {
		if got := netTemplateName(in); got != want {
			t.Errorf("netTemplateName(%q) = %q, want %q", in, got, want)
		}
		if got := baseTemplateName(netTemplateName(in)); got != baseTemplateName(in) {
			t.Errorf("baseTemplateName round trip for %q: %q", in, got)
		}
	}
}

// Attaching rewrites the reference and nothing else about it: the !ENV tag and
// the ${PWD} are how appjail finds the file at all, and losing either breaks
// every stack with a template, attached or not.
func TestSetDirectorNetworksRetargetsTemplate(t *testing.T) {
	seedNetwork(t)
	out, err := setDirectorNetworks(context.Background(), templateDirector, "immich",
		[]composepkg.Attachment{{Network: "vlan6"}})
	if err != nil {
		t.Fatalf("setDirectorNetworks: %v", err)
	}
	want := "- template: !ENV '${PWD}/immich-postgres-template.net.conf'"
	if !strings.Contains(out, want) {
		t.Fatalf("missing %q in:\n%s", want, out)
	}
	back, err := clearDirectorNetworks(out)
	if err != nil {
		t.Fatalf("clearDirectorNetworks: %v", err)
	}
	if want := "- template: !ENV '${PWD}/immich-postgres-template.conf'"; !strings.Contains(back, want) {
		t.Errorf("detach did not restore the template:\n%s", back)
	}
}

// Detaching has to take the per-service options off too. A project-level
// default cannot override an option a service still carries, so a stack taken
// off its network went on trying to attach to it.
func TestClearDirectorNetworksClearsServiceOptions(t *testing.T) {
	seedNetwork(t)
	attached, err := setDirectorNetworks(context.Background(), templateDirector, "immich",
		[]composepkg.Attachment{{Network: "vlan6"}})
	if err != nil {
		t.Fatalf("setDirectorNetworks: %v", err)
	}
	if !strings.Contains(attached, "bridge: 'epair:") {
		t.Fatalf("nothing to clear:\n%s", attached)
	}
	out, err := clearDirectorNetworks(attached)
	if err != nil {
		t.Fatalf("clearDirectorNetworks: %v", err)
	}
	for _, gone := range []string{"bridge: 'epair:", "ifconfig:", "dhcp:"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived the detach:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "virtualnet: ':<random> default'") {
		t.Errorf("did not go back to appjail's NAT network:\n%s", out)
	}
	// The bundle's own options are not fjord's to remove.
	for _, keep := range []string{"ghcr.io/daemonless/immich-postgres:latest", "immich_redis"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
}

func TestWriteNetTemplates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "immich-postgres-template.conf"), []byte(postgresTemplate), 0o644); err != nil {
		t.Fatal(err)
	}
	// A bundle shipping its own .net.conf owns that name; generating over it
	// would replace a real template with a derived one.
	mine := filepath.Join(dir, "hand.net.conf")
	if err := os.WriteFile(mine, []byte("ip4: inherit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withHand := templateDirector + "  extra:\n    name: immich_extra\n    options:\n" +
		"      - template: !ENV '${PWD}/hand.net.conf'\n"
	if err := ensureNetTemplates(dir, withHand); err != nil {
		t.Fatalf("ensureNetTemplates: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "immich-postgres-template.net.conf"))
	if err != nil {
		t.Fatalf("variant not written: %v", err)
	}
	if !strings.Contains(string(got), "sysvshm: new") || strings.Contains(string(got), "ip4: inherit") {
		t.Errorf("variant is wrong:\n%s", got)
	}
	if !strings.HasPrefix(string(got), "# Generated by fjord") {
		t.Errorf("variant does not say it is generated:\n%s", got)
	}
	if b, _ := os.ReadFile(mine); string(b) != "ip4: inherit\n" {
		t.Errorf("overwrote a template the bundle ships: %q", b)
	}
	// The original is left alone: it is what host networking needs.
	if b, _ := os.ReadFile(filepath.Join(dir, "immich-postgres-template.conf")); !strings.Contains(string(b), "ip4: inherit") {
		t.Errorf("the base template lost its ip4 line:\n%s", b)
	}
	// A reference to a file no bundle shipped is appjail's to complain about.
	if err := ensureNetTemplates(dir, "services:\n  a:\n    options:\n      - template: !ENV '${PWD}/nope.conf'\n"); err != nil {
		t.Errorf("missing template should be ignored here: %v", err)
	}
}

// Attachments are written per service now, so reading only the project's
// options reported an attached stack as being on no network at all -- and the
// same network, once per jail, is still one network.
func TestDirectorAttachmentsPerService(t *testing.T) {
	seedNetwork(t)
	attached, err := setDirectorNetworks(context.Background(), templateDirector, "immich",
		[]composepkg.Attachment{{Network: "vlan6"}})
	if err != nil {
		t.Fatalf("setDirectorNetworks: %v", err)
	}
	atts := directorAttachments(attached)
	if len(atts) != 1 {
		t.Fatalf("got %d attachments, want 1: %+v", len(atts), atts)
	}
	if atts[0].Network != "vlan6" {
		t.Errorf("network = %q, want vlan6", atts[0].Network)
	}
}

// The shape stage 1 exists for: the service people use is on the LAN, its
// database and cache are on a segment only it can reach.
func TestSetDirectorNetworksPerServiceAttachments(t *testing.T) {
	seedNetwork(t)
	out, err := setDirectorNetworks(context.Background(), templateDirector, "immich",
		[]composepkg.Attachment{
			{Network: "vlan6", Service: "immich-server"},
			{Network: "vlan5", Service: "immich-server", IP: "192.168.5.10"},
			{Network: "vlan5", Service: "database", IP: "192.168.5.11"},
			{Network: "vlan5", Service: "redis", IP: "192.168.5.12"},
		})
	if err != nil {
		t.Fatalf("setDirectorNetworks: %v", err)
	}
	// Each service holds its own address. Before per-service attachments only
	// the first could, and the rest fell back to a lease or were refused.
	for _, want := range []string{
		"ifconfig: 'sb_immichimmi01:192.168.5.10/24'",
		"ifconfig: 'sb_immichdatab2:192.168.5.11/24'",
		"ifconfig: 'sb_immichredis1:192.168.5.12/24'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Only immich-server is on the second network.
	if n := strings.Count(out, "vlan6bridge"); n != 1 {
		t.Errorf("vlan6 attached to %d services, want 1:\n%s", n, out)
	}
	// The database is on a network, so it takes the template variant.
	if !strings.Contains(out, "immich-postgres-template.net.conf") {
		t.Errorf("database did not get the networked template:\n%s", out)
	}
}

// A service no attachment names keeps what it had. Stripping it would take the
// other three jails off their network on a payload that only mentioned one.
func TestSetDirectorNetworksLeavesUnnamedServicesAlone(t *testing.T) {
	seedNetwork(t)
	out, err := setDirectorNetworks(context.Background(), templateDirector, "immich",
		[]composepkg.Attachment{{Network: "vlan6", Service: "immich-server"}})
	if err != nil {
		t.Fatalf("setDirectorNetworks: %v", err)
	}
	if n := strings.Count(out, "bridge: 'epair:"); n != 1 {
		t.Errorf("expected exactly one service attached, got %d:\n%s", n, out)
	}
	// redis and database still carry only what the bundle gave them.
	for _, keep := range []string{"ghcr.io/daemonless/redis:latest", "ghcr.io/daemonless/immich-postgres:latest"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
	// And the one left on host networking keeps its host-stack template.
	if !strings.Contains(out, "immich-postgres-template.conf") ||
		strings.Contains(out, "immich-postgres-template.net.conf") {
		t.Errorf("an unattached service should keep the base template:\n%s", out)
	}
}

// A typo in a service name puts a jail on nothing and reads as the network
// being broken, which is a long way from the cause.
func TestSetDirectorNetworksRejectsUnknownService(t *testing.T) {
	seedNetwork(t)
	_, err := setDirectorNetworks(context.Background(), templateDirector, "immich",
		[]composepkg.Attachment{{Network: "vlan6", Service: "postgres"}})
	if err == nil {
		t.Fatal("accepted an attachment for a service the director does not have")
	}
	if !strings.Contains(err.Error(), "no service named") {
		t.Errorf("unhelpful error: %v", err)
	}
}
