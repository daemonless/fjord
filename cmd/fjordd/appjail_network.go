package main

import (
	"fmt"
	"strconv"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/hostnet"

	"gopkg.in/yaml.v3"
)

// ifaceMax bounds the epair name fjord generates. appjail derives "sa_<name>"
// (host side) and "sb_<name>" (jail side) from it, and an interface name must
// fit IFNAMSIZ (16, including the NUL), so the base has 3 characters of prefix
// to spare.
const ifaceMax = 12

// epairName turns a stack id into a valid, deterministic epair name.
//
// It must be deterministic: appjail's "<random>" placeholder is resolved at
// run time, and the ifconfig option has to name "sb_<iface>" -- fjord cannot
// reference a name it will not know. Anything outside [a-z0-9] is dropped
// rather than substituted, since an interface name has a narrow charset.
func epairName(stackID string, n int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(stackID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if name == "" {
		name = "fjord"
	}
	// An interface name may not start with a digit on FreeBSD. Do this BEFORE
	// trimming: prefixing afterwards pushed the name over the limit again, and
	// the re-trim ate the suffix -- so every network of a stack whose id was
	// long enough and began with a digit got the same interface name.
	if name[0] >= '0' && name[0] <= '9' {
		name = "j" + name
	}
	// Each network needs its own epair, so the second and later ones carry a
	// suffix. The first keeps the bare name: it is what existing stacks have.
	suffix := ""
	if n > 0 {
		suffix = strconv.Itoa(n)
	}
	if len(name)+len(suffix) > ifaceMax {
		name = name[:ifaceMax-len(suffix)]
	}
	return name + suffix
}

// setDirectorNetwork rewrites a director document's top-level options so the
// project's jails sit on a host bridge with a fixed address, replacing
// appjail's default NAT virtualnet.
//
// It also drops every service's `expose:`. appjail refuses that outright
// alongside a bridge ("expose requires the following options: virtualnet"),
// and rightly so: a jail on its own address has no host port to forward.
// setDirectorNetworks puts a jail on every network in atts: one epair, and
// one address (or DHCP lease) per network. The first is the jail's primary.
func setDirectorNetworks(directorYML, stackID string, atts []composepkg.Attachment) (string, error) {
	if len(atts) == 0 {
		return "", fmt.Errorf("at least one network is required")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(directorYML), &doc); err != nil {
		return "", fmt.Errorf("parse director: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("director is not a YAML mapping")
	}
	root := doc.Content[0]

	var kvs [][2]string
	bpf := false
	seen := map[string]bool{}
	for i, a := range atts {
		// Two epairs onto the same bridge is two interfaces on one segment:
		// legal, and never what anyone means.
		if seen[a.Network] {
			return "", fmt.Errorf("network %q is listed twice", a.Network)
		}
		seen[a.Network] = true
		net, ok := hostnet.Get(a.Network)
		if !ok {
			return "", fmt.Errorf("no network named %q is defined on this host", a.Network)
		}
		if net.Bridge == "" {
			return "", fmt.Errorf("network %q has no bridge to attach a jail to", a.Network)
		}
		prefix := "24"
		if _, p, found := strings.Cut(net.Subnet, "/"); found {
			prefix = p
		}
		if !net.DHCP && net.Subnet == "" {
			return "", fmt.Errorf("network %q has no subnet to place a jail on", a.Network)
		}
		// A pool network is allocated by the podman side's IPAM, which appjail
		// cannot ask -- so an address has to be given. A DHCP network has no
		// pool at all: the jail asks the segment's server, as a container does.
		if a.IP == "" && !net.DHCP {
			return "", fmt.Errorf("an address is required to place a jail on %q: that network hands out addresses from a pool this host manages, and appjail cannot draw from it", a.Network)
		}

		iface := epairName(stackID, i)
		kvs = append(kvs, [2]string{"bridge", fmt.Sprintf("epair:%s bridge:%s", iface, net.Bridge)})
		if net.DHCP {
			// dhclient runs inside the jail and needs bpf, which appjail hides
			// by default -- without the rule the lease never arrives and rc
			// waits out defaultroute_delay with no address.
			kvs = append(kvs, [2]string{"dhcp", "sb_" + iface})
			bpf = true
		} else {
			kvs = append(kvs, [2]string{"ifconfig", fmt.Sprintf("sb_%s:%s/%s", iface, a.IP, prefix)})
			if i == 0 {
				// Only one default route, and it belongs to the first network.
				kvs = append(kvs, [2]string{"defaultrouter", net.Gateway})
			}
		}
		// appjail sets the MAC on the jail side of the epair, which is the end
		// a DHCP server sees -- so a reservation keyed on it resolves exactly
		// as it would for a physical host.
		if a.MAC != "" {
			kvs = append(kvs, [2]string{"macaddr", "sb_" + iface + ":" + a.MAC})
		}
	}
	if bpf {
		kvs = append(kvs, [2]string{"device", "path bpf unhide"})
	}

	opts := &yaml.Node{Kind: yaml.SequenceNode}
	for _, kv := range kvs {
		if kv[1] == "" {
			continue
		}
		opts.Content = append(opts.Content, &yaml.Node{
			Kind: yaml.MappingNode,
			Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Value: kv[0]},
				{Kind: yaml.ScalarNode, Value: kv[1], Style: yaml.SingleQuotedStyle},
			},
		})
	}
	setMapKey(root, "options", opts)
	dropServiceExpose(root)

	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	enc.Close()
	return sb.String(), nil
}

// dropServiceExpose removes every service's `expose:` option.
func dropServiceExpose(root *yaml.Node) {
	services := mapKey(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return
	}
	for i := 1; i < len(services.Content); i += 2 {
		svc := services.Content[i]
		opts := mapKey(svc, "options")
		if opts == nil || opts.Kind != yaml.SequenceNode {
			continue
		}
		kept := opts.Content[:0]
		for _, item := range opts.Content {
			if item.Kind == yaml.MappingNode && len(item.Content) >= 1 && item.Content[0].Value == "expose" {
				continue
			}
			kept = append(kept, item)
		}
		opts.Content = kept
	}
}

func mapKey(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func setMapKey(n *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = val
			return
		}
	}
	n.Content = append(n.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key}, val)
}
