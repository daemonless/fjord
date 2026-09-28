package lannet

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
)

// probeMu serializes probes: each one adds an interface to a bridge and runs a
// DHCP client, and two at once would only race for the same answer.
var probeMu sync.Mutex

// probeWait is how long a DHCP server gets to answer. An OFFER normally lands
// within a second or two; the cap keeps the form from hanging on a wire where
// nothing is listening.
const probeWait = 12 * time.Second

// ProbeSegment asks the DHCP server on a bridge's wire which segment it is.
//
// A bridge the host holds no address on tells fjord nothing about its subnet,
// so the form used to show an example, the operator typed something, and a
// network claiming 192.168.86.0/24 went onto a VLAN 4 wire -- its containers
// then replied to .86 hosts out the wrong wire and never answered them.
//
// The request goes out through a throwaway epair on the bridge, from a random
// locally administered MAC, with the same caged dhclient cni-epair uses: its
// own config and a script that records the lease and configures nothing, so
// the host's routes and resolv.conf are never touched.
func ProbeSegment(ctx context.Context, bridge string) (engine.Segment, error) {
	if bridge == "" || isRuntimeBridge(bridge) {
		return engine.Segment{}, fmt.Errorf("%q is not a bridge a LAN network can use", bridge)
	}
	if err := exec.CommandContext(ctx, "ifconfig", bridge).Run(); err != nil {
		return engine.Segment{}, fmt.Errorf("no bridge %q on this host", bridge)
	}
	probeMu.Lock()
	defer probeMu.Unlock()

	out, err := exec.CommandContext(ctx, "ifconfig", "epair", "create").Output()
	if err != nil {
		return engine.Segment{}, fmt.Errorf("creating a probe interface: %w", err)
	}
	a := strings.TrimSpace(string(out))
	if !strings.HasSuffix(a, "a") {
		return engine.Segment{}, fmt.Errorf("unexpected epair name %q", a)
	}
	b := strings.TrimSuffix(a, "a") + "b"
	// Destroying one end takes the pair with it, and leaves the bridge.
	defer exec.Command("ifconfig", a, "destroy").Run()

	steps := [][]string{
		{b, "ether", probeMAC()},
		{bridge, "addm", a},
		{a, "up"},
		{b, "up"},
	}
	for _, s := range steps {
		if msg, err := exec.CommandContext(ctx, "ifconfig", s...).CombinedOutput(); err != nil {
			return engine.Segment{}, fmt.Errorf("ifconfig %s: %s", strings.Join(s, " "), strings.TrimSpace(string(msg)))
		}
	}

	dir, err := os.MkdirTemp("", "fjord-probe-")
	if err != nil {
		return engine.Segment{}, err
	}
	defer os.RemoveAll(dir)
	result := filepath.Join(dir, "result")
	// The path is written into the script: dhclient builds the script's
	// environment itself, so nothing exported from here would arrive.
	script := "#!/bin/sh\ncase \"$reason\" in\nBOUND|RENEW|REBIND|REBOOT)\n" +
		"  printf '%s %s %s\\n' \"$new_ip_address\" \"$new_subnet_mask\" \"$new_routers\" > " + result + "\n" +
		"  ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "script"), []byte(script), 0o755); err != nil {
		return engine.Segment{}, err
	}
	conf := fmt.Sprintf("interface %q { script %q; }\n", b, filepath.Join(dir, "script"))
	if err := os.WriteFile(filepath.Join(dir, "conf"), []byte(conf), 0o644); err != nil {
		return engine.Segment{}, err
	}

	pctx, cancel := context.WithTimeout(ctx, probeWait)
	defer cancel()
	cmd := exec.CommandContext(pctx, "dhclient", "-d",
		"-c", filepath.Join(dir, "conf"), "-l", filepath.Join(dir, "lease"), "-p", filepath.Join(dir, "pid"), b)
	if err := cmd.Start(); err != nil {
		return engine.Segment{}, fmt.Errorf("starting dhclient: %w", err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	for {
		if line, err := os.ReadFile(result); err == nil && len(line) > 0 {
			return parseLease(string(line))
		}
		select {
		case <-pctx.Done():
			return engine.Segment{}, fmt.Errorf("nothing answered DHCP on %s within %s", bridge, probeWait)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// parseLease reads the probe script's "<address> <mask> <routers>" line.
func parseLease(line string) (engine.Segment, error) {
	f := strings.Fields(line)
	if len(f) < 2 {
		return engine.Segment{}, fmt.Errorf("DHCP answered without an address and mask: %q", strings.TrimSpace(line))
	}
	ip := net.ParseIP(f[0]).To4()
	maskIP := net.ParseIP(f[1]).To4()
	if ip == nil || maskIP == nil {
		return engine.Segment{}, fmt.Errorf("DHCP answered with %q, not an IPv4 address and mask", strings.TrimSpace(line))
	}
	mask := net.IPMask(maskIP)
	ones, bits := mask.Size()
	if bits == 0 {
		return engine.Segment{}, fmt.Errorf("DHCP answered with an invalid mask %s", f[1])
	}
	seg := engine.Segment{Subnet: fmt.Sprintf("%s/%d", ip.Mask(mask), ones)}
	if len(f) > 2 {
		seg.Gateway = f[2]
	}
	return seg, nil
}

// probeMAC is a random locally administered unicast address, so the probe's
// lease is never mistaken for a container's reservation.
func probeMAC() string {
	b := make([]byte, 6)
	rand.Read(b)
	b[0] = (b[0] &^ 0x01) | 0x02
	return net.HardwareAddr(b).String()
}
