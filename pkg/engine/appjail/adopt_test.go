package appjail

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixture: uptime-kuma on saturn, made by a director project outside fjord.
func TestConvertJail(t *testing.T) {
	info := jailInfo{
		Name: "uptime_kuma_uptime_kuma", Image: "ghcr.io/daemonless/uptime-kuma:latest", State: "running",
		Network: "ajnet", Address: "10.0.0.3", NAT: true,
		Exposes: []exposeEntry{{"3001", "3001", "tcp"}},
		Mounts:  []mountEntry{{"/containers/uptime-kuma/config", "/config", "<pseudofs>", "rw"}},
		Env:     map[string]string{"PUID": "1000", "PGID": "1000", "TZ": "UTC", "DATA_DIR": "/config"},
		User:    "root",
		Params: []string{
			`exec.start: "/bin/sh /etc/rc"`, `exec.stop: "/bin/sh /etc/rc.shutdown jail"`, "mount.devfs", "persist", "allow.raw_sockets",
			"vnet", `vnet.interface: "eb_1568ea0a609"`,
			`exec.prestart: "appjail network plug -e \"1568ea0a609\" -n \"ajnet\""`,
			`exec.poststart: "appjail network assign -d -e \"1568ea0a609\" -j \"${name}\" -n \"ajnet\""`,
			`exec.poststop: "appjail network unplug \"ajnet\" \"1568ea0a609\""`,
			`exec.prestart+: "appjail nat on jail \"${name}\""`, `exec.poststop+: "appjail nat off jail \"${name}\""`,
			`exec.prestart+: "appjail expose on \"${name}\""`, `exec.poststop+: "appjail expose off \"${name}\""`,
		},
	}
	spec := convertJail(info)
	if spec.Service != "uptime-kuma" {
		t.Errorf("service = %q", spec.Service)
	}
	for _, want := range []string{
		"  - virtualnet: 'ajnet:<random> default address:10.0.0.3'\n", "  - nat:\n",
		"    name: uptime_kuma_uptime_kuma\n", "      - expose: '3001:3001 proto:tcp'\n",
		"      user: root\n", "        - PUID: !ENV '${PUID}'\n", "        - DATA_DIR: /config\n",
		"      - UPTIME_KUMA_CONFIG_PATH: /config\n", "    device: !ENV '${UPTIME_KUMA_CONFIG_PATH}'\n",
	} {
		if !strings.Contains(spec.Director, want) {
			t.Errorf("director missing %q:\n%s", want, spec.Director)
		}
	}
	if !strings.Contains(spec.Makejail, "OPTION from=ghcr.io/daemonless/uptime-kuma:${tag}\n") || !strings.Contains(spec.Makejail, "ARG tag=latest\n") {
		t.Errorf("makejail:\n%s", spec.Makejail)
	}
	want := "# template.conf\n\nexec.start: \"/bin/sh /etc/rc\"\nexec.stop: \"/bin/sh /etc/rc.shutdown jail\"\nmount.devfs\npersist\nallow.raw_sockets\n"
	if spec.Template != want {
		t.Errorf("template kept plumbing:\n%s", spec.Template)
	}
	for _, want := range []string{"container_name: uptime_kuma_uptime_kuma\n", "      - \"${UPTIME_KUMA_CONFIG_PATH}:/config\"\n", "      - \"3001:3001\"\n", "      - PUID=${PUID}\n"} {
		if !strings.Contains(spec.Compose, want) {
			t.Errorf("compose missing %q:\n%s", want, spec.Compose)
		}
	}
	if spec.Env != "PGID=1000\nPUID=1000\nTZ=UTC\nUPTIME_KUMA_CONFIG_PATH=/containers/uptime-kuma/config\n" {
		t.Errorf("env:\n%s", spec.Env)
	}
	if len(spec.Notes) != 0 {
		t.Errorf("notes: %v", spec.Notes)
	}
}

// A jail whose image has no dhclient cannot request a lease, and saying the
// DHCP server did not answer sends someone to the wrong machine.
func TestMissingDHClient(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mk := func(jail string, withDHClient bool) {
		rcd := filepath.Join(root, jail, "jail", "etc", "rc.d")
		if err := os.MkdirAll(rcd, 0o755); err != nil {
			t.Fatal(err)
		}
		if withDHClient {
			if err := os.WriteFile(filepath.Join(rcd, "dhclient"), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("has", true)
	mk("lacks", false)

	old := jailsDirOverride
	jailsDirOverride = root
	t.Cleanup(func() { jailsDirOverride = old })

	if missingDHClient(ctx, "has") {
		t.Error("reported dhclient missing when it is there")
	}
	if !missingDHClient(ctx, "lacks") {
		t.Error("did not notice the image has no dhclient")
	}
	// A jail that is not mounted must not look like every image lacks it.
	if missingDHClient(ctx, "nosuchjail") {
		t.Error("answered for a jail whose filesystem is not there")
	}
}
