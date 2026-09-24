package main

import (
	"context"
	"fmt"
	"io"
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
// Called before an update, after anything that starts a stack, and at
// fjordd start for stacks already running. host-local hands out the next
// address after the last one it gave, not the lowest free, so a recreated
// container moves: tautulli went .200 -> .201 on an update and every
// bookmark broke. And its reservations live in /var/run, which a reboot
// empties: every pool address was handed out again from the bottom of the
// range (pooled .232 -> .230). Pinning the address a service already has is
// the only thing that survives a recreate, a stop, and a reboot.
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
		" -- pinned in the compose so it keeps this address\n"
}

// keepAfter streams out, then pins the addresses the stack came up with and
// appends what it pinned. At the end, not before: a service that was not
// running has no address until the stream has started it.
func (s *server) keepAfter(ctx context.Context, st *stack.Stack, out io.ReadCloser) io.ReadCloser {
	return &thenReader{r: out, c: out, after: func() string { return keptNote(s.keepAddresses(ctx, st)) }}
}

// thenReader reads r, then whatever after returns, once.
type thenReader struct {
	r     io.Reader
	c     io.Closer
	after func() string
	tail  io.Reader
}

func (t *thenReader) Read(p []byte) (int, error) {
	if t.tail != nil {
		return t.tail.Read(p)
	}
	n, err := t.r.Read(p)
	if err == io.EOF {
		t.tail = strings.NewReader(t.after())
		if n > 0 {
			return n, nil
		}
		return t.tail.Read(p)
	}
	return n, err
}

func (t *thenReader) Close() error { return t.c.Close() }
