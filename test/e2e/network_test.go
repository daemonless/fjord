//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	lanNet   = envOr("E2E_LAN", "lan")                  // a fjord pool network on this host
	lanSpare = envOr("E2E_LAN_SPARE", "192.168.86.249") // an address in its range nothing uses
	ipamDir  = "/var/run/cni/networks"
	dhcpNet  = envOr("E2E_DHCP", "lan-dhcp") // a fjord DHCP network on this host
)

// needLAN skips when the host has no pool network to test on.
func needLAN(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(ipamDir, lanNet)); err != nil {
		code, body := api(t, "GET", "/api/networks", nil)
		if code != 200 || !strings.Contains(body, `"name":"`+lanNet+`"`) {
			t.Skipf("no network %q here (set E2E_LAN)", lanNet)
		}
	}
}

// onLAN is a sleeper service attached to the pool network.
func onLAN(svc string) string {
	return fmt.Sprintf("services:\n  %s:\n    image: %s\n    entrypoint: [\"/bin/sleep\", \"3600\"]\n    networks:\n      - %s\nnetworks:\n  %s:\n    external: true\n",
		svc, fixtureRef, lanNet, lanNet)
}

// lanAddress is the address a stack's service holds on the pool network.
func lanAddress(t *testing.T, name, svc string) string {
	t.Helper()
	return lanAddressOn(t, name, svc, lanNet)
}

// lanAddressOn is the address a stack's service holds on one network.
func lanAddressOn(t *testing.T, name, svc, network string) string {
	t.Helper()
	_, body := api(t, "GET", "/api/stacks/"+name, nil)
	var d struct {
		Status struct {
			Containers []struct {
				Service   string
				Addresses map[string]string
			}
		}
	}
	decode(t, body, &d)
	for _, c := range d.Status.Containers {
		if c.Service == svc {
			return c.Addresses[network]
		}
	}
	return ""
}

// An update keeps a service on the address it had (#18: host-local hands out
// the next address, so tautulli moved .200 -> .201 and bookmarks broke). The
// pin is made at the first up; the update must hold to it.
func TestAddressKeptAcrossUpdate(t *testing.T) {
	needLAN(t)
	name := stack(t, onLAN("app"))
	action(t, name, "up", nil)
	before := lanAddress(t, name, "app")
	if before == "" {
		t.Fatalf("fixture: app got no address on %s", lanNet)
	}
	if out := action(t, name, "update", nil); failed(out) {
		t.Fatalf("update failed:\n%s", out)
	}
	if after := lanAddress(t, name, "app"); after != before {
		t.Errorf("address moved %s -> %s", before, after)
	}
	_, body := api(t, "GET", "/api/stacks/"+name, nil)
	if !strings.Contains(body, "ipv4_address: "+before) {
		t.Errorf("the address was not pinned in the compose")
	}
	// A second update has nothing to pin and must not move it either.
	if out := action(t, name, "update", nil); failed(out) || lanAddress(t, name, "app") != before {
		t.Errorf("second update moved it or failed:\n%s", out)
	}
}

// A pool address is pinned the first time a service comes up, so nothing
// that drops its reservation can move it: a reboot empties /var/run, and
// host-local then hands out from the bottom of the range (pooled .232 ->
// .230 on a netlab reboot). Down + up is the same move without the reboot:
// host-local gives the next address, not the one just released.
func TestAddressKeptAcrossRestart(t *testing.T) {
	needLAN(t)
	name := stack(t, onLAN("app"))
	out := action(t, name, "up", nil)
	addr := lanAddress(t, name, "app")
	if addr == "" {
		t.Fatalf("fixture: app got no address on %s", lanNet)
	}
	if !strings.Contains(out, "kept app on "+addr) {
		t.Errorf("first up did not pin %s:\n%s", addr, out)
	}
	action(t, name, "down", nil)
	if out := action(t, name, "up", nil); failed(out) {
		t.Fatalf("second up failed:\n%s", out)
	}
	if after := lanAddress(t, name, "app"); after != addr {
		t.Errorf("address moved %s -> %s across down + up", addr, after)
	}
}

// On a DHCP network the MAC is pinned, not the address: the lease follows
// the MAC, and an unpinned epair's MAC comes from its unit number. Another
// container taking that number first gave the service a new MAC and a new
// lease (.122 -> .120 on netlab).
func TestDHCPMACKept(t *testing.T) {
	if code, body := api(t, "GET", "/api/networks", nil); code != 200 || !strings.Contains(body, `"name":"`+dhcpNet+`"`) {
		t.Skipf("no DHCP network %q here (set E2E_DHCP)", dhcpNet)
	}
	name := stack(t, strings.ReplaceAll(onLAN("app"), lanNet, dhcpNet))
	out := action(t, name, "up", nil)
	addr := lanAddressOn(t, name, "app", dhcpNet)
	if addr == "" {
		t.Fatalf("fixture: no lease on %s:\n%s", dhcpNet, out)
	}
	if !strings.Contains(out, "kept app's MAC") {
		t.Errorf("first up did not pin the MAC:\n%s", out)
	}
	action(t, name, "down", nil)
	// Take the epair unit the service just released.
	hog := "e2e-epairhog"
	sh(t, "podman", "run", "-d", "--rm", "--name", hog, "--network", dhcpNet, "--entrypoint", "/bin/sleep", fixtureRef, "600")
	t.Cleanup(func() { shTry("podman", "rm", "-f", hog) })
	if out := action(t, name, "up", nil); failed(out) {
		t.Fatalf("second up failed:\n%s", out)
	}
	if after := lanAddressOn(t, name, "app", dhcpNet); after != addr {
		t.Errorf("lease moved %s -> %s once another container took the epair", addr, after)
	}
}

// A reservation held by a container that no longer exists is released at the
// next start on that network (#18: old cni-epair leaked one per recreate;
// jupiter had 16+).
func TestOrphanReservationReleased(t *testing.T) {
	needLAN(t)
	res := filepath.Join(ipamDir, lanNet, lanSpare)
	if _, err := os.Stat(res); err == nil {
		t.Skipf("%s is really reserved here (set E2E_LAN_SPARE)", lanSpare)
	}
	// An orphan, as the old plugin left them: a gone container's ID, an hour old.
	sh(t, "sh", "-c", fmt.Sprintf(`printf 'e2e0000000000000000000000000000000000000000000000000000000000000\r\neth0' | %s tee %s >/dev/null && %s touch -t %s %s`,
		sudo, res, sudo, time.Now().Add(-time.Hour).Format("200601021504"), res))
	t.Cleanup(func() { shTry(sudo, "rm", "-f", res) })

	name := stack(t, onLAN("app"))
	out := action(t, name, "up", nil)
	if !strings.Contains(out, "released "+lanSpare+" on "+lanNet) {
		t.Errorf("the orphan was not reported released:\n%s", out)
	}
	// Gone, or taken again by this stack's own container: host-local hands
	// out the next address, which can be the one just freed.
	if b, err := os.ReadFile(res); err == nil && strings.HasPrefix(string(b), "e2e0000") {
		t.Errorf("%s is still held by the orphan", lanSpare)
	}
}

// A fresh reservation is left alone even when its container isn't listed
// yet: another stack may be starting that very moment.
func TestFreshReservationKept(t *testing.T) {
	needLAN(t)
	res := filepath.Join(ipamDir, lanNet, lanSpare)
	if _, err := os.Stat(res); err == nil {
		t.Skipf("%s is really reserved here", lanSpare)
	}
	sh(t, "sh", "-c", fmt.Sprintf(`printf 'e2e1111111111111111111111111111111111111111111111111111111111111\r\neth0' | %s tee %s >/dev/null`, sudo, res))
	t.Cleanup(func() { shTry(sudo, "rm", "-f", res) })

	name := stack(t, onLAN("app"))
	if out := action(t, name, "up", nil); strings.Contains(out, "released "+lanSpare) {
		t.Errorf("a seconds-old reservation was released:\n%s", out)
	}
	if _, err := os.Stat(res); err != nil {
		t.Errorf("%s was removed", lanSpare)
	}
}

// An app installed from the catalog onto the LAN gets an address of its own
// and answers on it -- the install path, not only save + up.
func TestInstallOnLAN(t *testing.T) {
	needLAN(t)
	manifest := sh(t, "fetch", "-qo", "-", "https://catalog.daemonless.io/v1/daemonless/manifests/zensical.yaml")
	name := fmt.Sprintf("e2e-inst-%d", time.Now().Unix()%10000)
	t.Cleanup(func() {
		api(t, "DELETE", "/api/stacks/"+name, nil)
		shTry(sudo, "rm", "-rf", "/containers/"+name)
	})
	code, out := api(t, "POST", "/api/apps/install", map[string]any{
		"name": name, "app_id": "zensical", "manifest": manifest, "engine": "podman",
		"values":   map[string]string{"WEB_PORT": "8000"},
		"networks": []map[string]string{{"network": lanNet, "service": "zensical"}},
	})
	if code != 200 || failed(out) {
		t.Fatalf("install: %d\n%s", code, out)
	}
	addr := lanAddress(t, name, "zensical")
	if addr == "" {
		t.Fatalf("installed with no address on %s", lanNet)
	}
	var err error
	for i := 0; i < 30; i++ { // s6 starts the app a few seconds after the container
		var r *http.Response
		if r, err = http.Get("http://" + addr + ":8000/"); err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Errorf("zensical on %s never answered: %v", addr, err)
}

// fjordRoot is where fjordd keeps its stacks on the host under test.
var fjordRoot = envOr("E2E_FJORD_ROOT", "/var/db/fjord")

// pinnedOnLAN is onLAN with the service's address pinned.
func pinnedOnLAN(svc, ip string) string {
	return fmt.Sprintf("services:\n  %s:\n    image: %s\n    entrypoint: [\"/bin/sleep\", \"3600\"]\n    networks:\n      %s:\n        ipv4_address: %s\nnetworks:\n  %s:\n    external: true\n",
		svc, fixtureRef, lanNet, ip, lanNet)
}

// An address another stack has pinned is refused on Save, and -- for a
// compose changed behind fjord's back -- at start (#24: on jupiter smokeping
// and seerr both answered for 192.168.5.19).
func TestAddressClashRefused(t *testing.T) {
	needLAN(t)
	first := stack(t, pinnedOnLAN("app", lanSpare))
	second := stack(t, onLAN("app")) // on the network, no pin yet

	code, body := api(t, "POST", "/api/stacks/"+second+"/save", map[string]any{"compose": pinnedOnLAN("app", lanSpare), "env": ""})
	if code != 409 || !strings.Contains(body, lanSpare) || !strings.Contains(body, first+"'s address") {
		t.Errorf("save of a taken address: %d %s", code, body)
	}

	// The same compose written straight to disk: pre-flight catches it.
	path := filepath.Join(fjordRoot, "stacks", second, "compose.yaml")
	sh(t, sudo, "sh", "-c", fmt.Sprintf("cat > %s <<'EOF'\n%sEOF", path, pinnedOnLAN("app", lanSpare)))
	code, body = api(t, "POST", "/api/stacks/"+second+"/up", nil)
	if code != 409 || !strings.Contains(body, first+"'s address") {
		t.Errorf("start with a taken address: %d %s", code, body)
	}
}

// A service on a pool network and a DHCP network on the same segment holds
// both addresses, and fjord says so (#28: the lease sat inside the pool's
// subnet and was attributed to neither -- "no address").
func TestPoolAndDHCPOnOneSegment(t *testing.T) {
	needLAN(t)
	if code, body := api(t, "GET", "/api/networks", nil); code != 200 || !strings.Contains(body, `"name":"`+dhcpNet+`"`) {
		t.Skipf("no DHCP network %q here (set E2E_DHCP)", dhcpNet)
	}
	compose := fmt.Sprintf("services:\n  app:\n    image: %s\n    entrypoint: [\"/bin/sleep\", \"3600\"]\n    networks:\n      - %s\n      - %s\nnetworks:\n  %s:\n    external: true\n  %s:\n    external: true\n",
		fixtureRef, lanNet, dhcpNet, lanNet, dhcpNet)
	name := stack(t, compose)
	if out := action(t, name, "up", nil); failed(out) {
		t.Fatalf("up:\n%s", out)
	}
	pool, lease := lanAddressOn(t, name, "app", lanNet), lanAddressOn(t, name, "app", dhcpNet)
	if pool == "" || lease == "" || pool == lease {
		t.Errorf("addresses: %s=%q %s=%q", lanNet, pool, dhcpNet, lease)
	}
	_, body := api(t, "GET", "/api/stacks/"+name, nil)
	if strings.Contains(body, "no address") {
		t.Errorf("reported as having no address:\n%s", body)
	}
}

// An app installed from the catalog onto a DHCP network gets a lease, answers
// on it, and has its MAC pinned so the lease survives a recreate.
func TestInstallOnDHCP(t *testing.T) {
	if code, body := api(t, "GET", "/api/networks", nil); code != 200 || !strings.Contains(body, `"name":"`+dhcpNet+`"`) {
		t.Skipf("no DHCP network %q here (set E2E_DHCP)", dhcpNet)
	}
	manifest := sh(t, "fetch", "-qo", "-", "https://catalog.daemonless.io/v1/daemonless/manifests/zensical.yaml")
	name := fmt.Sprintf("e2e-dhcpinst-%d", time.Now().Unix()%10000)
	t.Cleanup(func() {
		api(t, "DELETE", "/api/stacks/"+name, nil)
		shTry(sudo, "rm", "-rf", "/containers/"+name)
	})
	code, out := api(t, "POST", "/api/apps/install", map[string]any{
		"name": name, "app_id": "zensical", "manifest": manifest, "engine": "podman",
		"values":   map[string]string{"WEB_PORT": "8000"},
		"networks": []map[string]string{{"network": dhcpNet, "service": "zensical"}},
	})
	if code != 200 || failed(out) {
		t.Fatalf("install: %d\n%s", code, out)
	}
	addr := lanAddressOn(t, name, "zensical", dhcpNet)
	if addr == "" {
		t.Fatalf("installed with no lease on %s:\n%s", dhcpNet, out)
	}
	_, body := api(t, "GET", "/api/stacks/"+name, nil)
	if !strings.Contains(body, "mac_address") {
		t.Errorf("the MAC was not pinned after install")
	}
	var err error
	for i := 0; i < 30; i++ {
		var r *http.Response
		if r, err = http.Get("http://" + addr + ":8000/"); err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Errorf("zensical on its lease %s never answered: %v", addr, err)
}
