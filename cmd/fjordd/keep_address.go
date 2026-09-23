package main

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/stack"
)

// keepAddresses pins, in the compose, the address each service holds right
// now on a fjord pool network, and returns what it pinned.
//
// Called before an update. host-local hands out the next address after the
// last one it gave, not the lowest free, so a recreated container moves:
// tautulli went .200 -> .201 on an update and every bookmark broke. Pinning
// the address it already has is the only thing that survives a recreate --
// and a stop, which removes the containers too.
//
// Left alone: addresses already pinned (the operator's), DHCP networks (the
// router's reservation keeps those), static ones (nothing allocates), and a
// stack's private segment (its services find each other by name).
func (s *server) keepAddresses(ctx context.Context, st *stack.Stack) []string {
	be := s.backendFor(st)
	if !be.Capabilities().UpdateServices || st.Compose == "" {
		return nil // podman only: appjail's director names no compose networks
	}
	status, err := be.Status(ctx, st)
	if err != nil {
		return nil
	}
	private := map[string]bool{}
	if list, err := s.manager.List(); err == nil {
		for _, other := range list {
			private[privateNetworkName(other.Name)] = true
		}
	}
	held := map[string]map[string]string{} // service -> network -> address
	for _, c := range status.Containers {
		if c.Service != "" && c.Addresses != nil {
			held[c.Service] = c.Addresses
		}
	}
	compose := st.Compose
	var kept []string
	per := composepkg.ServiceAttachments(compose)
	svcs := make([]string, 0, len(per))
	for svc := range per {
		svcs = append(svcs, svc)
	}
	sort.Strings(svcs)
	for _, svc := range svcs {
		for _, a := range per[svc] {
			if a.IP != "" || private[a.Network] {
				continue
			}
			n, ok := hostnet.Get(a.Network)
			if !ok || n.DHCP || n.Static {
				continue
			}
			ip := held[svc][a.Network]
			if parsed := net.ParseIP(ip); parsed == nil || parsed.To4() == nil {
				continue
			}
			out, err := composepkg.PinAddress(compose, svc, a.Network, ip)
			if err != nil || out == compose {
				continue
			}
			compose = out
			kept = append(kept, fmt.Sprintf("kept %s on %s (%s)", svc, ip, a.Network))
		}
	}
	if len(kept) == 0 {
		return nil
	}
	st.Compose = compose
	if err := s.manager.Save(st); err != nil {
		return []string{"could not keep addresses: " + err.Error()}
	}
	return kept
}

// keptNote is the Output preamble for keepAddresses.
func keptNote(kept []string) string {
	if len(kept) == 0 {
		return ""
	}
	return "[fjord] " + strings.Join(kept, "\n[fjord] ") +
		" -- pinned in the compose so the update doesn't move it\n"
}
