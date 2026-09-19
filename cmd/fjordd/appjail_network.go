package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine/appjail"
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
func setDirectorNetworks(ctx context.Context, directorYML, stackID string, atts []composepkg.Attachment) (string, error) {
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
	onBridge := map[string]string{}
	for i, a := range atts {
		if seen[a.Network] {
			return "", fmt.Errorf("network %q is listed twice", a.Network)
		}
		seen[a.Network] = true
		// An appjail virtual network is joined with the `virtualnet` option,
		// exactly as appjail documents it: virtualnet="<network>:<interface>".
		// It is not a conflist and has no host bridge, so it never resolves
		// below -- fjord could create one and then refuse to put anything on
		// it, with "no network named ... is defined on this host".
		if vnet, isVirtual := appjail.Virtualnet(ctx, a.Network); isVirtual {
			if seenBridge := onBridge[a.Network]; seenBridge != "" {
				return "", fmt.Errorf("network %q is listed twice", a.Network)
			}
			onBridge[a.Network] = a.Network
			// appjail-quick(1): virtualnet="[network]:interface [default]
			// [address:ipv4-address]". A pinned address goes in the option --
			// it was being dropped, the jail taking whatever appjail's pool
			// handed it while the UI showed the address the user typed.
			// appjail validates it against the network's CIDR and against
			// every other jail's reservation, so there is nothing to check
			// here that it does not check better.
			vn := a.Network + ":" + epairName(stackID, i)
			if a.IP != "" {
				vn += " address:" + a.IP
			}
			kvs = append(kvs, [2]string{"virtualnet", vn})
			if i == 0 && vnet.Gateway != "" {
				kvs = append(kvs, [2]string{"nat", ""})
			}
			continue
		}
		net, ok := hostnet.Get(a.Network)
		if !ok {
			return "", fmt.Errorf("no network named %q is defined on this host", a.Network)
		}
		if net.Bridge == "" {
			return "", fmt.Errorf("network %q has no bridge to attach a jail to", a.Network)
		}
		prefix := ""
		if _, p, found := strings.Cut(net.Subnet, "/"); found {
			prefix = p
		}
		// Guessing /24 onto a segment fjord has never seen would put the jail
		// on the wrong mask and break it in a way nobody would look for here.
		if a.IP != "" && prefix == "" {
			return "", fmt.Errorf("fjord does not know which segment %q is on, so it cannot place %s there: appjail configures the interface itself and needs the prefix length. Give the network a subnet, or leave the address blank and let the jail take a lease", a.Network, a.IP)
		}
		if !net.DHCP && net.Subnet == "" {
			return "", fmt.Errorf("network %q has no subnet to place a jail on", a.Network)
		}
		// A pool network is allocated by the podman side's IPAM, which appjail
		// cannot ask -- so an address has to be given. A DHCP network has no
		// pool at all: the jail asks the segment's server, as a container does.
		if a.IP == "" && !net.DHCP {
			why := "that network draws from a range set aside for podman's IPAM, which appjail cannot ask"
			if net.Static {
				why = "nothing allocates on that network -- every stack brings its own address"
			}
			return "", fmt.Errorf("an address is required to place a jail on %q: %s", a.Network, why)
		}

		// Two epairs onto the same bridge is two interfaces on one segment:
		// pointless, and appjail will not do it -- the jail fails to create,
		// with the director reporting only "FAIL!". Caught here so the answer
		// names the two networks rather than leaving a stack that will not
		// start. (Verified on FreeBSD 15.1: two epairs on DIFFERENT bridges
		// are fine; two on the same one are not.)
		if other, dup := onBridge[net.Bridge]; dup {
			return "", fmt.Errorf("%q and %q are both on bridge %s, and appjail cannot put a jail on one bridge twice -- they are the same segment, so one of them is enough", other, a.Network, net.Bridge)
		}
		onBridge[net.Bridge] = a.Network

		iface := epairName(stackID, i)
		kvs = append(kvs, [2]string{"bridge", fmt.Sprintf("epair:%s bridge:%s", iface, net.Bridge)})
		// DHCP or a fixed address is a per-stack choice, not a property of the
		// network: the wire is the same either way, and so is the mechanism --
		// `ifconfig sb_x:<addr>/<prefix>`, exactly as on a range network. The
		// only thing a DHCP network used to lack was the prefix length, which
		// fjord had declined to write down; it is recorded now, so pinning an
		// address here is the same operation it is anywhere else.
		if net.DHCP && a.IP == "" {
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
	// Keep the options fjord does not own. The first cut replaced the whole
	// list, which threw away anything a bundle had put there -- and made the
	// original unrecoverable, so there is nothing to put back when the jail
	// later leaves the bridge.
	setDirectorOptions(root, mergeDirectorOptions(root, opts))
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

// directorNetOptions are the option keys setDirectorNetworks writes. They are
// replaced wholesale each time it runs; everything else in the list is the
// bundle's and is carried through untouched.
//
// virtualnet and nat are in here because they are the thing a bridge REPLACES:
// a jail cannot be on appjail's NAT network and a host bridge at once.
var directorNetOptions = map[string]bool{
	"bridge": true, "dhcp": true, "ifconfig": true, "defaultrouter": true,
	"macaddr": true, "device": true, "virtualnet": true, "nat": true,
}

// mergeDirectorOptions puts the generated networking options after whatever
// the document already had, minus the networking options it had.
func mergeDirectorOptions(root, generated *yaml.Node) *yaml.Node {
	out := &yaml.Node{Kind: yaml.SequenceNode}
	if cur := mapKey(root, "options"); cur != nil && cur.Kind == yaml.SequenceNode {
		for _, item := range cur.Content {
			// Each option is a one-key mapping: `- bridge: 'epair:… bridge:…'`.
			if item.Kind == yaml.MappingNode && len(item.Content) >= 1 && directorNetOptions[item.Content[0].Value] {
				continue
			}
			out.Content = append(out.Content, item)
		}
	}
	out.Content = append(out.Content, generated.Content...)
	return out
}

// clearDirectorNetworks takes a project's jails off their host bridges and
// back onto appjail's own NAT network.
//
// The virtualnet/nat pair written here is appjail's default, NOT a restore of
// what the bundle had: setDirectorNetworks used to replace the option list
// outright, so for any stack that has been attached once the original is gone.
// Every fjord bundle starts on this pair, so it is the right answer -- it is
// just arrived at by knowing appjail rather than by remembering.
func clearDirectorNetworks(directorYML string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(directorYML), &doc); err != nil {
		return "", fmt.Errorf("parse director: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("director is not a YAML mapping")
	}
	root := doc.Content[0]
	def := &yaml.Node{Kind: yaml.SequenceNode}
	for _, kv := range [][2]string{{"virtualnet", ":<random> default"}, {"nat", ""}} {
		v := &yaml.Node{Kind: yaml.ScalarNode, Value: kv[1], Style: yaml.SingleQuotedStyle}
		if kv[1] == "" {
			v = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
		}
		def.Content = append(def.Content, &yaml.Node{
			Kind:    yaml.MappingNode,
			Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: kv[0]}, v},
		})
	}
	setDirectorOptions(root, mergeDirectorOptions(root, def))

	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	enc.Close()
	return sb.String(), nil
}

// directorAttachments reads back the networks a director bundle actually puts
// its jails on.
//
// For a director stack the director IS the networking truth -- appjail never
// reads compose.yaml for it -- so the Resources tab has to be built from this
// rather than from the compose, which otherwise shows four interfaces for a
// jail that has one.
//
// The link back to a network name is the bridge: the epair name encodes the
// interface index, not which network it joined.
func directorAttachments(directorYML string) []composepkg.Attachment {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(directorYML), &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	opts := mapKey(doc.Content[0], "options")
	if opts == nil || opts.Kind != yaml.SequenceNode {
		return nil
	}
	byBridge := map[string]string{}
	for _, d := range hostnet.List() {
		if d.Bridge != "" {
			if _, dup := byBridge[d.Bridge]; !dup {
				byBridge[d.Bridge] = d.Name
			}
		}
	}
	var out []composepkg.Attachment
	byIface := map[string]int{}
	for _, item := range opts.Content {
		if item.Kind != yaml.MappingNode || len(item.Content) < 2 {
			continue
		}
		key, val := item.Content[0].Value, item.Content[1].Value
		switch key {
		case "bridge":
			// `epair:<iface> bridge:<bridge>`
			var iface, br string
			for _, f := range strings.Fields(val) {
				if v, ok := strings.CutPrefix(f, "epair:"); ok {
					iface = v
				} else if v, ok := strings.CutPrefix(f, "bridge:"); ok {
					br = v
				}
			}
			if iface == "" {
				continue
			}
			byIface[iface] = len(out)
			out = append(out, composepkg.Attachment{Network: byBridge[br], Iface: "sb_" + iface})
		case "virtualnet":
			// `<network>:<iface> [default] [address:<ip>]`. An empty network
			// name is appjail's own default NAT -- the bridge state, not a
			// network anyone attached to -- so it is skipped. Without this
			// case a stack on a named virtual network reported no networks at
			// all and read as though it were on the default bridge.
			netName, rest, _ := strings.Cut(val, ":")
			if netName == "" {
				continue
			}
			iface, _, _ := strings.Cut(rest, " ")
			att := composepkg.Attachment{Network: netName, Iface: "eb_" + iface}
			for _, f := range strings.Fields(rest) {
				if v, ok := strings.CutPrefix(f, "address:"); ok {
					att.IP = v
				}
			}
			out = append(out, att)
		case "ifconfig":
			// `sb_<iface>:<ip>/<prefix>`
			if name, rest, ok := strings.Cut(strings.TrimPrefix(val, "sb_"), ":"); ok {
				if i, found := byIface[name]; found {
					out[i].IP, _, _ = strings.Cut(rest, "/")
				}
			}
		case "macaddr":
			// `sb_<iface>:<mac>`
			if name, mac, ok := strings.Cut(strings.TrimPrefix(val, "sb_"), ":"); ok {
				if i, found := byIface[name]; found {
					out[i].MAC = mac
				}
			}
		}
	}
	return out
}

// setDirectorOptions writes the option list, removing the key when the list is
// empty.
//
// An empty `options:` is not the same as no options: appjail-director reads it
// as null and dies with "'NoneType' object is not iterable" before it starts
// anything. Verified on the VM.
func setDirectorOptions(root, opts *yaml.Node) {
	if opts == nil || len(opts.Content) == 0 {
		mapDelete(root, "options")
		return
	}
	setMapKey(root, "options", opts)
}

// disableDirectorNetworks puts a project's jails on no network at all.
//
// There is no director option for it -- it is the ABSENCE of one. A jail with
// no network option gets no vnet and no address: verified on the VM, nothing
// reaches it, it reaches nothing, and its published ports answer nowhere. The
// expose lines go too, since appjail refuses them without a network to publish
// on ("expose requires the following options: virtualnet").
func disableDirectorNetworks(directorYML string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(directorYML), &doc); err != nil {
		return "", fmt.Errorf("parse director: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("director is not a YAML mapping")
	}
	root := doc.Content[0]
	setDirectorOptions(root, mergeDirectorOptions(root, &yaml.Node{Kind: yaml.SequenceNode}))
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

// mapDelete removes a key and its value from a mapping node.
func mapDelete(n *yaml.Node, key string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
}
