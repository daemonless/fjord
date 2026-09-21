package main

import (
	"context"
	"errors"
	"net"
	"testing"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
)

// immich's declaration: the server on whatever the install chose, everything
// else on a segment only this stack can reach.
var immichPlan = map[string]string{
	"immich-server": "default",
	"*":             "private",
}

var immichServices = []string{"immich-server", "immich-machine-learning", "redis", "database"}

func TestPlanServiceNetworks(t *testing.T) {
	made := 0
	atts, _, err := planServiceNetworks(context.Background(), immichPlan,
		[]composepkg.Attachment{{Network: "lan", IP: "192.168.4.90"}},
		immichServices,
		func() (string, error) { made++; return "immich_private", nil })
	if err != nil {
		t.Fatalf("planServiceNetworks: %v", err)
	}
	got := map[string]composepkg.Attachment{}
	nets := map[string][]string{}
	for _, a := range atts {
		if _, seen := got[a.Service]; !seen {
			got[a.Service] = a
		}
		nets[a.Service] = append(nets[a.Service], a.Network)
	}
	// Five, not four: the exposed service is on the network it was given AND
	// on the private one, or it cannot reach its own database.
	if len(atts) != 5 {
		t.Fatalf("got %d attachments, want 5: %+v", len(atts), atts)
	}
	if len(nets["immich-server"]) != 2 {
		t.Errorf("immich-server is on %v, want its network and the private one", nets["immich-server"])
	}
	// The one people open keeps the chosen network AND its address.
	if a := got["immich-server"]; a.Network != "lan" || a.IP != "192.168.4.90" {
		t.Errorf("immich-server = %+v, want lan/192.168.4.90", a)
	}
	// The rest are private, and the address the user gave is NOT copied onto
	// them -- one address describes one interface.
	for _, svc := range []string{"redis", "database", "immich-machine-learning"} {
		if a := got[svc]; a.Network != "immich_private" || a.IP != "" {
			t.Errorf("%s = %+v, want immich_private with no address", svc, a)
		}
	}
	// Made once for the stack, not once per service.
	if made != 1 {
		t.Errorf("created the private network %d times, want 1", made)
	}
}

// No declaration means the old behaviour: whatever was chosen, unchanged.
func TestPlanServiceNetworksNoPlan(t *testing.T) {
	chosen := []composepkg.Attachment{{Network: "lan"}}
	atts, _, err := planServiceNetworks(context.Background(), nil, chosen, immichServices,
		func() (string, error) { t.Fatal("must not create a network"); return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 || atts[0].Service != "" {
		t.Errorf("a stack with no plan should be left alone: %+v", atts)
	}
}

// A plan naming a service the stack does not have is a typo, and silently it
// would just mean some service got no network.
func TestPlanServiceNetworksUnknownService(t *testing.T) {
	_, _, err := planServiceNetworks(context.Background(),
		map[string]string{"postgres": "private", "*": "default"},
		[]composepkg.Attachment{{Network: "lan"}}, immichServices,
		func() (string, error) { return "immich_private", nil })
	if err == nil {
		t.Fatal("accepted a plan naming a service that does not exist")
	}
}

// The private network is only created when something actually asks for one.
func TestPlanServiceNetworksNoPrivateWanted(t *testing.T) {
	atts, _, err := planServiceNetworks(context.Background(),
		map[string]string{"*": "default"},
		[]composepkg.Attachment{{Network: "lan"}}, immichServices,
		func() (string, error) { t.Fatal("must not create a network"); return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != len(immichServices) {
		t.Errorf("got %d attachments, want one per service", len(atts))
	}
}

// If the segment cannot be made, the install fails saying so rather than
// quietly putting the database on the LAN.
func TestPlanServiceNetworksPrivateFails(t *testing.T) {
	_, _, err := planServiceNetworks(context.Background(), immichPlan,
		[]composepkg.Attachment{{Network: "lan"}}, immichServices,
		func() (string, error) { return "", errors.New("no bridge") })
	if err == nil {
		t.Fatal("an install that cannot make the private network must not continue")
	}
}

func TestPrivateNetworkName(t *testing.T) {
	for in, want := range map[string]string{
		"immich":                  "immich_priv",
		"immich-2":                "immich-2_priv",
		"My Stack!":               "My-Stack_priv",
		"-weird-":                 "weird_priv",
		"averyverylongstackname":  "averyveryl_priv",
		"trailing-separator--xyz": "trailing-s_priv",
		"!!!":                     "stack_priv",
	} {
		if got := privateNetworkName(in); got != want {
			t.Errorf("privateNetworkName(%q) = %q, want %q", in, got, want)
		}
	}
	// appjail makes the bridge with this name, and an interface name must fit
	// IFNAMSIZ. 16 is refused outright: "network name too long".
	for _, in := range []string{"immich", "immich-2", "a-really-long-stack-name-indeed", "x"} {
		if n := privateNetworkName(in); len(n) > privateNetNameMax {
			t.Errorf("privateNetworkName(%q) = %q (%d chars), over the %d limit", in, n, len(n), privateNetNameMax)
		}
	}
}

func TestFreePrivateSubnet(t *testing.T) {
	got, err := freePrivateSubnet(nil)
	if err != nil {
		t.Fatalf("freePrivateSubnet: %v", err)
	}
	_, ipnet, err := net.ParseCIDR(got)
	if err != nil {
		t.Fatalf("returned %q, which is not a CIDR: %v", got, err)
	}
	// Never podman's default network: a private segment overlapping 10.88/16
	// takes the host's own containers down with it.
	_, podman, _ := net.ParseCIDR("10.88.0.0/16")
	if overlapsAny(ipnet, []*net.IPNet{podman}) {
		t.Errorf("%s overlaps podman's 10.88.0.0/16", got)
	}
	// And never a segment this host is already on.
	if overlapsAny(ipnet, hostSubnets()) {
		t.Errorf("%s overlaps something this host is already on", got)
	}
}

// A segment another stack already holds must not be handed out again --
// including appjail's own networks, which are not conflists and whose bridge
// exists only while a jail is up, so nothing else on the host reveals them.
func TestFreePrivateSubnetAvoidsEngineNetworks(t *testing.T) {
	first, err := freePrivateSubnet(nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := freePrivateSubnet([]engine.Network{{Name: "immich_priv", Subnet: first}})
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Errorf("handed out %s twice", first)
	}
}

func TestOverlapsAny(t *testing.T) {
	cidr := func(s string) *net.IPNet { _, n, _ := net.ParseCIDR(s); return n }
	taken := []*net.IPNet{cidr("10.88.0.0/16"), cidr("192.168.4.0/24")}
	for in, want := range map[string]bool{
		"10.88.5.0/24":   true,  // inside podman's
		"10.100.0.0/24":  false, // clear
		"192.168.4.0/24": true,  // exactly a taken one
		"192.168.0.0/16": true,  // contains a taken one
	} {
		if got := overlapsAny(cidr(in), taken); got != want {
			t.Errorf("overlapsAny(%s) = %v, want %v", in, got, want)
		}
	}
}

// The wizard names a network per service and sends addresses for the ones that
// landed on a real network. The plan still decides placement -- sending only
// the addressed services must not drop the private ones.
func TestPlanServiceNetworksPerServiceWithAddresses(t *testing.T) {
	plan := map[string]string{
		"immich-server":           "vlan5",
		"immich-machine-learning": "private",
		"redis":                   "private",
		"database":                "private",
	}
	atts, _, err := planServiceNetworks(context.Background(), plan,
		[]composepkg.Attachment{{Network: "vlan5", Service: "immich-server", IP: "192.168.5.10", MAC: "02:1a:2b:3c:4d:5e"}},
		immichServices,
		func() (string, error) { return "immich_private", nil })
	if err != nil {
		t.Fatalf("planServiceNetworks: %v", err)
	}
	if len(atts) != 5 {
		t.Fatalf("got %d attachments, want 5 (the exposed one joins private too): %+v", len(atts), atts)
	}
	by := map[string]composepkg.Attachment{}
	for _, a := range atts {
		if _, seen := by[a.Service]; !seen {
			by[a.Service] = a
		}
	}
	// The address goes on the network it was given for, not on the private one.
	if a := by["immich-server"]; a.Network != "vlan5" || a.IP != "192.168.5.10" || a.MAC == "" {
		t.Errorf("immich-server lost its address: %+v", a)
	}
	for _, svc := range []string{"redis", "database", "immich-machine-learning"} {
		if a := by[svc]; a.Network != "immich_private" {
			t.Errorf("%s = %+v, want immich_private", svc, a)
		}
	}
}

// The built-ins are modes on a service, not networks it joins. Dropping them
// from the picker left no way to keep an app on the host stack it ships on.
func TestPlanServiceNetworksBuiltIns(t *testing.T) {
	atts, modes, err := planServiceNetworks(context.Background(),
		map[string]string{
			"immich-server":           "host",
			"immich-machine-learning": "bridge",
			"redis":                   "private",
			"database":                "none",
		},
		nil, immichServices,
		func() (string, error) { return "immich_private", nil })
	if err != nil {
		t.Fatalf("planServiceNetworks: %v", err)
	}
	// Only the one on a real network becomes an attachment.
	if len(atts) != 1 || atts[0].Service != "redis" || atts[0].Network != "immich_private" {
		t.Errorf("attachments = %+v, want redis on immich_private only", atts)
	}
	for svc, want := range map[string]string{
		"immich-server": "host", "immich-machine-learning": "bridge", "database": "none",
	} {
		if modes[svc] != want {
			t.Errorf("modes[%s] = %q, want %q", svc, modes[svc], want)
		}
	}
	if _, ok := modes["redis"]; ok {
		t.Errorf("redis is attached, so it has no mode: %+v", modes)
	}
}

// The install wizard's per-service editor sends its rows and NO plan: the
// operator already answered per service, so the list is the answer. Before
// this, a no-plan call returned the list untouched -- including the literal
// spec "private", which reached the engine as a network name that does not
// exist ("no network named private").
func TestPlanServiceNetworksNoPlanResolvesPrivate(t *testing.T) {
	made := 0
	atts, modes, err := planServiceNetworks(context.Background(), nil,
		[]composepkg.Attachment{
			{Network: "lan", Service: "immich-server", IP: "192.168.4.90"},
			{Network: "private", Service: "immich-server"},
			{Network: "private", Service: "database"},
		},
		immichServices,
		func() (string, error) { made++; return "immich_priv", nil })
	if err != nil {
		t.Fatalf("planServiceNetworks: %v", err)
	}
	if made != 1 {
		t.Errorf("made the private segment %d times, want once", made)
	}
	if modes == nil {
		t.Fatal("modes is nil; the handler merges the request's own modes into it and would panic")
	}
	want := []composepkg.Attachment{
		{Network: "lan", Service: "immich-server", IP: "192.168.4.90"},
		{Network: "immich_priv", Service: "immich-server"},
		{Network: "immich_priv", Service: "database"},
	}
	if len(atts) != len(want) {
		t.Fatalf("got %d attachments, want %d: %+v", len(atts), len(want), atts)
	}
	for i := range want {
		if atts[i] != want[i] {
			t.Errorf("attachment %d = %+v, want %+v", i, atts[i], want[i])
		}
	}
}

// A service the editor left with no interfaces is not silently dropped: the
// list says nothing about it, so nothing here invents one.
func TestPlanServiceNetworksNoPlanLeavesUnlistedServicesAlone(t *testing.T) {
	atts, _, err := planServiceNetworks(context.Background(), nil,
		[]composepkg.Attachment{{Network: "lan", Service: "immich-server"}},
		immichServices,
		func() (string, error) { return "immich_priv", nil })
	if err != nil {
		t.Fatalf("planServiceNetworks: %v", err)
	}
	if len(atts) != 1 || atts[0].Service != "immich-server" {
		t.Fatalf("got %+v, want only immich-server", atts)
	}
}

// A service on the private segment ONLY is reachable from this host and
// nowhere else, so it has to keep the ports it publishes. One that is also on
// a real segment does not: it has an address of its own there.
func TestPrivateOnlyServices(t *testing.T) {
	atts := []composepkg.Attachment{
		{Network: "lan", Service: "immich-server"},
		{Network: "immich_priv", Service: "immich-server"},
		{Network: "immich_priv", Service: "database"},
		{Network: "immich_priv", Service: "redis"},
	}
	got := privateOnlyServices(atts, "immich_priv")
	want := []string{"database", "redis"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	// The whole stack on the private segment: every service keeps its ports,
	// which is the only way a host with no attachable network can be used.
	all := []composepkg.Attachment{
		{Network: "immich_priv", Service: "immich-server"},
		{Network: "immich_priv", Service: "database"},
	}
	if got := privateOnlyServices(all, "immich_priv"); len(got) != 2 {
		t.Errorf("got %v, want both services", got)
	}
	// Nothing private at all: nothing to give back.
	if got := privateOnlyServices(atts, ""); got != nil {
		t.Errorf("got %v, want nothing", got)
	}
}

// An install that asks only for modes -- every service kept on the
// arrangement the app ships with, which is what a host with no attachable
// network gets -- sends no attachments at all. The mode map still has to come
// back non-nil: the handler merges the request's own modes into it, and a nil
// map there panics rather than failing.
func TestPlanServiceNetworksModesOnly(t *testing.T) {
	atts, modes, err := planServiceNetworks(context.Background(), nil, nil, immichServices,
		func() (string, error) { t.Fatal("must not create a network"); return "", nil })
	if err != nil {
		t.Fatalf("planServiceNetworks: %v", err)
	}
	if atts != nil {
		t.Errorf("got %+v attachments, want none", atts)
	}
	if modes == nil {
		t.Fatal("modes is nil; the handler assigns into it and would panic")
	}
	modes["immich-server"] = "host" // the assignment the handler makes
}
