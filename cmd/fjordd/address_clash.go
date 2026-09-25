package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/stack"
)

// addrHolder is who else has an address on a network.
type addrHolder struct {
	Stack, Service string
	Pinned         bool // written in its compose; else only held right now
}

// takenAddresses is every address another stack has, keyed network|address:
// the ones pinned in its compose, running or not, and the ones its running
// containers hold without a pin.
//
// Both kinds matter. On jupiter smokeping had 192.168.5.19 pinned inside
// vlan5's pool, and seerr, unpinned, was handed .19 by the pool when it was
// recreated -- two jails answering for one address, one of them unreachable.
// Nothing noticed, and pinning the addresses at start would have written .19
// into seerr's compose too, making it permanent.
func (s *server) takenAddresses(ctx context.Context, self string) map[string]addrHolder {
	taken := map[string]addrHolder{}
	list, err := s.manager.List()
	if err != nil {
		return taken
	}
	for _, o := range list {
		if o.Name == self {
			continue
		}
		full, err := s.manager.Get(o.Name)
		if err != nil {
			continue
		}
		for svc, atts := range composepkg.ServiceAttachments(full.Compose) {
			for _, a := range atts {
				for _, ip := range []string{a.IP, a.IP6} {
					if ip != "" {
						taken[a.Network+"|"+ip] = addrHolder{o.Name, svc, true}
					}
				}
			}
		}
		status, err := s.backendFor(full).Status(ctx, full)
		if err != nil {
			continue
		}
		for _, c := range status.Containers {
			for network, ip := range c.Addresses {
				if _, pinned := taken[network+"|"+ip]; !pinned && ip != "" {
					taken[network+"|"+ip] = addrHolder{o.Name, c.Service, false}
				}
			}
		}
	}
	return taken
}

// clashes lists, in words, each address a stack pins that another stack has.
func clashes(st *stack.Stack, taken map[string]addrHolder) []string {
	var out []string
	for svc, atts := range composepkg.ServiceAttachments(st.Compose) {
		for _, a := range atts {
			for _, ip := range []string{a.IP, a.IP6} {
				if h, ok := taken[a.Network+"|"+ip]; ok && ip != "" {
					out = append(out, clashText(svc, ip, a.Network, h))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func clashText(svc, ip, network string, h addrHolder) string {
	whose := "is " + h.Stack + "'s address"
	if !h.Pinned {
		whose = "is in use by " + h.Stack
	}
	if h.Service != "" && h.Service != h.Stack {
		whose += " (" + h.Service + ")"
	}
	return fmt.Sprintf("%s on %s %s -- give %s another address in the Services tab", ip, network, whose, svc)
}

// clashNote is the Output preamble for addresses keepAddresses would not pin.
func clashNote(skipped []string) string {
	if len(skipped) == 0 {
		return ""
	}
	return "[fjord] not pinned, the address is taken: " + strings.Join(skipped, "\n[fjord] not pinned, the address is taken: ") + "\n"
}
