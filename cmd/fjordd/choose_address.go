package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"runtime"
	"sort"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/lannet"
	"github.com/daemonless/fjord/pkg/stack"
)

// addressAnswers is the wire check chooseAddresses asks, wireCheckable
// whether it can answer here, linuxHost whether to try; tests set all three.
var (
	addressAnswers = lannet.Answers
	wireCheckable  = lannet.CanCheck
	linuxHost      = runtime.GOOS == "linux"
)

// maxProbes bounds how many held addresses one service steps past before
// fjord leaves the choice to the pool: each free answer takes 2 s.
const maxProbes = 8

// chooseAddresses gives each service about to come up on a pool network,
// with no address pinned and none held, an address nothing else has, and
// pins it -- before the engine starts it.
//
// A pool hands out the next address it has not given itself. It cannot know
// one another machine holds: it gave a container .4, which another host on
// the LAN had, and keepAddresses then pinned that for good. Only on a LAN
// network (macvlan, ipvlan). Skipped: the gateway, this host's own
// addresses, any another stack has on the same subnet, and any that answers
// on the wire. Where the wire cannot be checked the pool chooses, as before:
// on FreeBSD silently, on Linux with a line saying what to install.
func (s *server) chooseAddresses(ctx context.Context, st *stack.Stack) string {
	be := s.backendFor(st)
	if caps := be.Capabilities(); !caps.PinServices || caps.KeepsAddresses || st.Compose == "" || !linuxHost {
		return ""
	}
	type want struct{ svc, network string }
	var wants []want
	nets := map[string]hostnet.Network{}
	per := composepkg.ServiceAttachments(st.Compose)
	for svc, atts := range per {
		for _, a := range atts {
			if a.IP != "" {
				continue
			}
			n, ok := hostnet.Get(a.Network)
			if !ok || !onTheLAN(n) || n.DHCP || n.Static || n.Subnet == "" || n.Bridge == "" || s.isPrivateNetwork(a.Network) {
				continue
			}
			nets[a.Network] = n
			wants = append(wants, want{svc, a.Network})
		}
	}
	if len(wants) == 0 {
		return ""
	}
	if !wireCheckable() {
		return fmt.Sprintf("[warn] %s on %s: fjord could not check that the address the pool gives is free on the wire -- install iputils' arping (Debian: iputils-arping) so it can\n", wants[0].svc, wants[0].network)
	}
	// One that already holds an address keeps it: keepAddresses pins that.
	if status, err := be.Status(ctx, st); err == nil {
		holds := map[string]bool{}
		for _, c := range status.Containers {
			for network, ip := range c.Addresses {
				if ip != "" {
					holds[c.Service+"|"+network] = true
				}
			}
		}
		kept := wants[:0]
		for _, w := range wants {
			if !holds[w.svc+"|"+w.network] {
				kept = append(kept, w)
			}
		}
		wants = kept
	}
	if len(wants) == 0 {
		return ""
	}
	sort.Slice(wants, func(i, j int) bool { return wants[i].svc+wants[i].network < wants[j].svc+wants[j].network })

	// What is taken, by subnet: two pool networks on one wire share it.
	taken := map[string]map[string]bool{}
	mark := func(subnet, ip string) {
		if taken[subnet] == nil {
			taken[subnet] = map[string]bool{}
		}
		taken[subnet][ip] = true
	}
	for key := range s.takenAddresses(ctx, st.Name) {
		network, ip, _ := strings.Cut(key, "|")
		if n, ok := hostnet.Get(network); ok && n.Subnet != "" {
			mark(n.Subnet, ip)
		}
	}
	own := hostAddresses()

	var notes []string
	type pin struct{ svc, network, ip string }
	var pins []pin
	for _, w := range wants {
		n := nets[w.network]
		var stepped []string
		chosen := ""
		for _, ip := range poolAddresses(n) {
			if ip == n.Gateway || own[ip] || taken[n.Subnet][ip] {
				continue
			}
			held, checked := addressAnswers(ctx, n.Bridge, ip)
			if !checked {
				log.Printf("%s: %s on %s: the wire could not be checked; the pool chooses", st.Name, w.svc, w.network)
				break
			}
			if held {
				stepped = append(stepped, ip)
				if len(stepped) >= maxProbes {
					break
				}
				continue
			}
			chosen = ip
			break
		}
		if chosen == "" {
			if len(stepped) > 0 {
				notes = append(notes, fmt.Sprintf("[warn] %s on %s: %s; left to the pool -- check its range", w.svc, w.network, othersHave(stepped)))
			}
			continue
		}
		pins = append(pins, pin{w.svc, w.network, chosen})
		mark(n.Subnet, chosen)
		if len(stepped) > 0 {
			notes = append(notes, fmt.Sprintf("[fjord] gave %s %s on %s: %s", w.svc, chosen, w.network, othersHave(stepped)))
		}
	}
	if len(pins) == 0 {
		return strings.Join(notes, "\n") + nl(notes)
	}
	// Onto the compose as it is on disk now, as keepAddresses does.
	saved, err := s.manager.Update(st.Name, func(fresh *stack.Stack) error {
		for _, p := range pins {
			out, err := composepkg.PinAddress(fresh.Compose, p.svc, p.network, p.ip)
			if err != nil {
				return err
			}
			fresh.Compose = out
		}
		return nil
	})
	if err != nil {
		return "[warn] could not pin the chosen addresses: " + err.Error() + "\n"
	}
	st.Compose = saved.Compose
	return strings.Join(notes, "\n") + nl(notes)
}

// othersHave says which addresses answered on the wire.
func othersHave(ips []string) string {
	if len(ips) == 1 {
		return ips[0] + " answers on the wire, so another machine has it"
	}
	return strings.Join(ips, ", ") + " answer on the wire, so other machines have them"
}

func nl(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return "\n"
}

// onTheLAN is true for a network whose containers sit on the LAN beside
// other machines. A NAT bridge's segment is this host's alone: nothing else
// can hold an address there.
func onTheLAN(n hostnet.Network) bool { return n.Type == "macvlan" || n.Type == "ipvlan" }

// isPrivateNetwork is true for a stack's own private segment: its services
// find each other by name, and nothing outside it shares the range.
func (s *server) isPrivateNetwork(network string) bool {
	list, err := s.manager.List()
	if err != nil {
		return false
	}
	for _, other := range list {
		if privateNetworkName(other.Name) == network {
			return true
		}
	}
	return false
}

// poolAddresses lists a pool network's addresses in order: its range, or
// the whole subnet less its network and broadcast addresses. IPv4 only.
func poolAddresses(n hostnet.Network) []string {
	_, subnet, err := net.ParseCIDR(n.Subnet)
	if err != nil || subnet.IP.To4() == nil {
		return nil
	}
	base := binary.BigEndian.Uint32(subnet.IP.To4())
	ones, bits := subnet.Mask.Size()
	size := uint32(1) << uint(bits-ones)
	lo, hi := base+1, base+size-2
	if size <= 2 {
		return nil
	}
	if ip := net.ParseIP(n.RangeStart).To4(); ip != nil && subnet.Contains(ip) {
		lo = max(lo, binary.BigEndian.Uint32(ip))
	}
	if ip := net.ParseIP(n.RangeEnd).To4(); ip != nil && subnet.Contains(ip) {
		hi = min(hi, binary.BigEndian.Uint32(ip))
	}
	var out []string
	for v := lo; v <= hi && v >= lo; v++ {
		b := make(net.IP, 4)
		binary.BigEndian.PutUint32(b, v)
		out = append(out, b.String())
	}
	return out
}

// hostAddresses is every IPv4 address this host holds.
func hostAddresses() map[string]bool {
	out := map[string]bool{}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
			out[ipn.IP.String()] = true
		}
	}
	return out
}
