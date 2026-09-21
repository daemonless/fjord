// Package compose provides minimal, structure-preserving edits to a stack's
// compose.yaml -- enough for fjord to wire up host provisioning (networks
// today) without taking over authorship of the file.
package compose

import (
	"bytes"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// InjectNetwork attaches an existing external container network to a stack's
// compose so its service gets its own routable IP instead of publishing on the
// host's ports. It adds a top-level `networks: {<network>: {external: true}}`
// and, on each service, a `networks:` entry.
//
// If ip is non-empty it sets ipv4_address on the stack's ONE published service
// -- the one with a `ports:` entry, which is what a fixed address is for. The
// remaining services join the same network and let IPAM assign them addresses,
// so they can still reach it. When no single service publishes ports there is
// nothing to pin the address to, and InjectNetwork says so.
//
// A stack whose services use `network_mode` is refused: those share the host's
// (or another container's) stack and address each other over localhost, so
// attaching a network both conflicts with network_mode and breaks that wiring.
//
// The yaml.Node round-trip preserves comments and key order. A service that
// already declares `networks` is left for the user rather than guessing a
// merge -- InjectNetwork returns an error naming it.
// Attachment is one network a stack joins, with the address and MAC pinned on
// it (both optional -- empty means "let the network decide").
type Attachment struct {
	Network string `json:"network"`
	IP      string `json:"ip,omitempty"`
	MAC     string `json:"mac,omitempty"`
	// Iface is what the interface is called INSIDE the container, reported by
	// the daemon and never written to the compose. podman names them eth0,
	// eth1, ...; appjail names the jail side of the epair after the option
	// that made it, so the same row is sb_<name> on a host bridge and
	// eb_<name> on a virtual network. Showing "eth0" for a jail was simply
	// wrong -- `ifconfig eth0` there answers "interface eth0 does not exist".
	Iface string `json:"iface,omitempty"`
}

// InjectNetwork attaches a stack to a single network. Thin wrapper over
// InjectNetworks for the common case.
func InjectNetwork(composeYAML, network, ip, mac string) (string, error) {
	return InjectNetworks(composeYAML, []Attachment{{Network: network, IP: ip, MAC: mac}})
}

// InjectNetworks puts a stack's services on the given networks, in order: the
// first becomes eth0 and is the one an address is read from.
//
// The address and MAC are written per network rather than per service, which
// is what the compose spec says and what podman-compose turns into
// `--network <name>:ip=...,mac=...` -- a service-level mac_address can only
// describe one interface.
//
// The attachment list is the whole truth: whatever the services declared
// before is replaced by it. The caller reads the current set with
// AttachedNetworks, so what is written is what the user was shown.
func InjectNetworks(composeYAML string, atts []Attachment) (string, error) {
	if len(atts) == 0 {
		return "", fmt.Errorf("at least one network is required")
	}
	seen := map[string]bool{}
	pinned := false
	for _, a := range atts {
		if a.Network == "" {
			return "", fmt.Errorf("network name is required")
		}
		if seen[a.Network] {
			return "", fmt.Errorf("network %q is listed twice", a.Network)
		}
		seen[a.Network] = true
		if a.MAC != "" && !macRe.MatchString(a.MAC) {
			return "", fmt.Errorf("invalid MAC address %q: want six hex pairs like 02:1a:2b:3c:4d:5e", a.MAC)
		}
		if a.IP != "" || a.MAC != "" {
			pinned = true
		}
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return "", fmt.Errorf("parse compose: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose is not a YAML mapping")
	}
	root := doc.Content[0]

	services := mapGet(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose has no services")
	}

	// Collect (name, node) for each service; service values sit at odd indices.
	type svc struct {
		name string
		node *yaml.Node
	}
	var svcs []svc
	for i := 0; i+1 < len(services.Content); i += 2 {
		svcs = append(svcs, svc{services.Content[i].Value, services.Content[i+1]})
	}
	if len(svcs) == 0 {
		return "", fmt.Errorf("compose has no services")
	}
	// network_mode and networks are mutually exclusive, and a stack wired this
	// way reaches its own parts over localhost -- moving it onto a network
	// gives every service a separate address and breaks all of it.
	for _, sv := range svcs {
		m := mapGet(sv.node, "network_mode")
		if sv.node.Kind != yaml.MappingNode || m == nil {
			continue
		}
		// "none" and "host" are fjord's own doing -- both are choices on the
		// stack's network picker -- so attaching is how either is undone.
		//
		// host with several services used to be refused here. The hazard is
		// real: under "host" they share one stack and DB_HOST=localhost works
		// TODAY, so giving each its own address breaks a running stack at
		// runtime. But refusing outright also made the choice unreachable for
		// a stack whose services address each other by name, and left no way
		// to say "I know". The operator is told what breaks, on the page, and
		// decides; fjord no longer decides for them.
		//
		// Until networking is per service, this is all-or-nothing: every
		// service lands on the network, not just the one that serves.
		if m.Value == None || m.Value == Host {
			clearMode(sv.node)
			continue
		}
		return "", fmt.Errorf("service %q uses network_mode: %s, which fjord does not know how to move onto a network", sv.name, m.Value)
	}
	// The stash exists to survive a trip through a mode, and attaching is the
	// end of that trip. It is dropped whether or not the caller used it: the
	// attachments written here are the literal truth, and a stash left behind
	// would shadow the next one.
	//
	// Deliberately NOT merged into the request. Filling a blank address from
	// it would resurrect a pin the user may have just cleared, and would leave
	// no way to say "attach lan with no address at all".
	for _, sv := range svcs {
		if sv.node.Kind == yaml.MappingNode {
			mapDelete(sv.node, "x-fjord-networks")
		}
	}

	// A pinned address or MAC belongs to the service that serves: with several
	// services it can only go on the published one.
	target := svcs[0].name
	if pinned && len(svcs) > 1 {
		var published []string
		for _, sv := range svcs {
			if sv.node.Kind == yaml.MappingNode && mapGet(sv.node, "ports") != nil {
				published = append(published, sv.name)
			}
		}
		if len(published) != 1 {
			return "", fmt.Errorf("this stack has %d services and %d of them publish ports, so there is no single service to pin an address or MAC to -- attach without pinning either, or set ipv4_address/mac_address yourself", len(svcs), len(published))
		}
		target = published[0]
	}

	// Top-level networks: {<network>: {external: true}} (idempotent).
	networks := mapEnsure(root, "networks")
	for _, a := range atts {
		if mapGet(networks, a.Network) == nil {
			mapSet(networks, a.Network, externalNetworkNode())
		}
	}

	for _, s := range svcs {
		if s.node.Kind != yaml.MappingNode {
			return "", fmt.Errorf("service %q is not a mapping", s.name)
		}
		use := atts
		if s.name != target {
			// Only the target carries pins; the others just join.
			use = make([]Attachment, len(atts))
			for i, a := range atts {
				use[i] = Attachment{Network: a.Network}
			}
		}
		mapSet(s.node, "networks", serviceNetworksNode(use))
		// A service-level mac_address can only describe one interface, and
		// podman-compose warns when it conflicts with a per-network one.
		mapDelete(s.node, "mac_address")
		nameSelf(s.node, s.name)
		stashPorts(s.node)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	enc.Close()
	return buf.String(), nil
}

// --- yaml.Node helpers ---

func mapGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapSet(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, scalar(key), val)
}

// mapEnsure returns the mapping node at key, creating an empty one if absent.
func mapEnsure(m *yaml.Node, key string) *yaml.Node {
	if v := mapGet(m, key); v != nil {
		return v
	}
	nv := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, scalar(key), nv)
	return nv
}

func scalar(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// externalNetworkNode builds {external: true}.
func externalNetworkNode() *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	n.Content = append(n.Content, scalar("external"),
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
	return n
}

// serviceNetworkNode builds the per-service networks value, using the two forms
// verified against podman-compose on FreeBSD: list form for auto-assign,
// mapping form with ipv4_address for a predictable IP.
// stashPorts takes a service's published ports out of service.
//
// A stack with its own address does not need them: the app is reachable at
// that address on its own port. Worse, podman publishes them anyway -- conmon
// binds the host port even for a container on a LAN network -- so two stacks
// that each have their own IP still collide on the host, and the second one
// fails pre-flight with a port conflict that does not exist.
//
// They are kept rather than deleted: the host port is the user's choice, and
// detaching the network should be able to give it back.
func stashPorts(svc *yaml.Node) {
	ports := mapGet(svc, "ports")
	if ports == nil {
		return
	}
	if mapGet(svc, "x-fjord-published") == nil {
		mapSet(svc, "x-fjord-published", ports)
	}
	mapDelete(svc, "ports")
}

// stashNetworks keeps a service's attachments where putting it on a mode would
// otherwise throw them away. Same contract as stashPorts, and the same guard
// for the same reason: lan,lan2 -> none -> host must not let the second
// transition overwrite the real stash with the empty one it finds.
//
// The stash is normalised to a mapping of network -> pins rather than a copy
// of whatever `networks:` held, so the service-level mac_address (the old
// single-network form, and the one a DHCP reservation is keyed on) has
// somewhere to go. Without that it is the most expensive thing on the page to
// lose, and it would vanish silently.
func stashNetworks(svc *yaml.Node) {
	atts := serviceAttachments(mapGet(svc, "networks"))
	if len(atts) == 0 || mapGet(svc, "x-fjord-networks") != nil {
		return
	}
	if atts[0].MAC == "" {
		if m := mapGet(svc, "mac_address"); m != nil {
			atts[0].MAC = m.Value
		}
	}
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, a := range atts {
		pins := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if a.IP != "" {
			pins.Content = append(pins.Content, scalar("ipv4_address"), scalar(a.IP))
		}
		if a.MAC != "" {
			pins.Content = append(pins.Content, scalar("mac_address"), scalar(a.MAC))
		}
		out.Content = append(out.Content, scalar(a.Network), pins)
	}
	mapSet(svc, "x-fjord-networks", out)
}

// StashedNetworks reports the attachments a stack had before it was put on a
// mode, so the UI can offer them back rather than making the user retype an
// address set it still has on disk.
func StashedNetworks(composeYAML string) []Attachment {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(composeYAML), &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	services := mapGet(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil
	}
	// The first service that has one: they are written together and read back
	// as one set, the same as AttachedNetworks reports one set for the stack.
	for i := 1; i < len(services.Content); i += 2 {
		if atts := serviceAttachments(mapGet(services.Content[i], "x-fjord-networks")); len(atts) > 0 {
			return atts
		}
	}
	return nil
}

// mapDelete removes a key and its value from a mapping node.
func mapDelete(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// nameSelf gives a service a hostname it can resolve.
//
// On an epair network podman has no address for the container -- the plugin is
// third-party, so podman never parses the conflist -- and the /etc/hosts it
// writes therefore has no entry for the container's own name. Anything that
// resolves its own hostname at startup dies on it: homepage exits with
// "getaddrinfo ENOTFOUND <container id>" and s6 restarts it forever.
//
// The name maps to 0.0.0.0, not to 127.0.0.1: servers that bind $HOSTNAME
// (Next.js does) must end up listening on the container's LAN address, and
// loopback would leave them reachable only from inside the jail. A self
// connect to 0.0.0.0 still lands on localhost. Neither key is touched when
// the stack already sets it.
func nameSelf(svc *yaml.Node, name string) {
	if mapGet(svc, "hostname") == nil {
		mapSet(svc, "hostname", scalar(name))
	}
	if mapGet(svc, "extra_hosts") == nil {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		seq.Content = append(seq.Content, scalar(name+":0.0.0.0"))
		mapSet(svc, "extra_hosts", seq)
	}
}

// serviceNetworksNode renders the service-level `networks:` key. A plain list
// when nothing is pinned -- the form most compose files already use -- and a
// mapping as soon as any entry carries an address or a MAC.
func serviceNetworksNode(atts []Attachment) *yaml.Node {
	plain := true
	for _, a := range atts {
		if a.IP != "" || a.MAC != "" {
			plain = false
		}
	}
	if plain {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, a := range atts {
			seq.Content = append(seq.Content, scalar(a.Network))
		}
		return seq
	}
	outer := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, a := range atts {
		inner := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if a.IP != "" {
			inner.Content = append(inner.Content, scalar("ipv4_address"), scalar(a.IP))
		}
		if a.MAC != "" {
			inner.Content = append(inner.Content, scalar("mac_address"), scalar(a.MAC))
		}
		if len(inner.Content) == 0 {
			inner.Style = yaml.FlowStyle // "net: {}" -- an empty mapping, not null
		}
		outer.Content = append(outer.Content, scalar(a.Network), inner)
	}
	return outer
}

func serviceNetworkNode(network, ip string) *yaml.Node {
	if ip == "" {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		seq.Content = append(seq.Content, scalar(network))
		return seq
	}
	inner := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	inner.Content = append(inner.Content, scalar("ipv4_address"), scalar(ip))
	outer := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	outer.Content = append(outer.Content, scalar(network), inner)
	return outer
}

// declaredNetworks counts the networks a service's `networks:` key names, in
// either compose form: a list of names, or a mapping of name to options.
func declaredNetworks(n *yaml.Node) int {
	switch n.Kind {
	case yaml.SequenceNode:
		return len(n.Content)
	case yaml.MappingNode:
		return len(n.Content) / 2
	}
	return 0
}

// macRe bounds a MAC address: six colon- or dash-separated hex pairs. It ends
// up in a compose file and on an ifconfig command line.
var macRe = regexp.MustCompile(`^([0-9a-fA-F]{2}[:-]){5}[0-9a-fA-F]{2}$`)

// AttachedMAC reports the MAC a stack pins, empty when it pins none.
// DetachNetworks takes a stack back off its networks and gives back what
// attaching took away: the published ports it stashed, and the self-hostname
// it added. Without this, removing the last network left a stack that showed
// as detached and still had every networks: key it started with.
func DetachNetworks(composeYAML string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return "", fmt.Errorf("parse compose: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose is not a YAML mapping")
	}
	root := doc.Content[0]
	services := mapGet(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose has no services")
	}

	for i := 0; i+1 < len(services.Content); i += 2 {
		name, svc := services.Content[i].Value, services.Content[i+1]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		stashNetworks(svc)
		mapDelete(svc, "networks")
		mapDelete(svc, "mac_address")
		// Bridge is reached by asking for nothing, so a mode fjord set has to
		// come off here -- without this, none -> bridge wrote the compose back
		// unchanged and the stack stayed isolated.
		clearMode(svc)
		if p := mapGet(svc, "x-fjord-published"); p != nil {
			mapSet(svc, "ports", p)
			mapDelete(svc, "x-fjord-published")
		}
		// Only the entry attaching added: anything else there is the user's.
		if h := mapGet(svc, "extra_hosts"); h != nil && h.Kind == yaml.SequenceNode {
			kept := h.Content[:0]
			for _, e := range h.Content {
				if e.Value != name+":0.0.0.0" {
					kept = append(kept, e)
				}
			}
			h.Content = kept
			if len(h.Content) == 0 {
				mapDelete(svc, "extra_hosts")
			}
		}
	}

	// Top-level declarations of networks nothing joins any more. Only the
	// external ones -- an internal network is part of the stack's own design.
	if nets := mapGet(root, "networks"); nets != nil && nets.Kind == yaml.MappingNode {
		kept := nets.Content[:0]
		for i := 0; i+1 < len(nets.Content); i += 2 {
			if v := nets.Content[i+1]; v == nil || v.Kind != yaml.MappingNode || mapGet(v, "external") == nil {
				kept = append(kept, nets.Content[i], nets.Content[i+1])
			}
		}
		nets.Content = kept
		if len(nets.Content) == 0 {
			mapDelete(root, "networks")
		}
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	enc.Close()
	return buf.String(), nil
}

// AttachedNetworks reports every network the stack's services are on, in
// order, with the address and MAC pinned on each -- the inverse of
// InjectNetworks, so the UI can show what is there and hand it straight back.
//
// Read from the service carrying the pins (the published one), because that is
// the service InjectNetworks writes them to.
func AttachedNetworks(composeYAML string) []Attachment {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(composeYAML), &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	services := mapGet(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil
	}
	// Every service must be on the same networks. When they disagree the
	// table cannot represent it, and writing one service's set back would
	// silently move the others -- so report nothing and leave the compose to
	// be edited by hand.
	var names []string
	var pinned []Attachment
	first := true
	for i := 0; i+1 < len(services.Content); i += 2 {
		svc := services.Content[i+1]
		if svc.Kind != yaml.MappingNode || mapGet(svc, "network_mode") != nil {
			return nil // host/none/container: -- not a named network
		}
		atts := serviceAttachments(mapGet(svc, "networks"))
		// A stack attached before per-network pins carries one service-level
		// mac_address, which podman-compose applies to the first network. Read
		// it the same way, or it vanishes from the UI and is dropped on save.
		if len(atts) > 0 && atts[0].MAC == "" {
			if m := mapGet(svc, "mac_address"); m != nil {
				atts[0].MAC = m.Value
			}
		}
		got := make([]string, len(atts))
		for j, a := range atts {
			got[j] = a.Network
		}
		if first {
			names, first = got, false
		} else if !sameOrder(names, got) {
			return nil
		}
		if pinCount(atts) > 0 {
			if pinned != nil {
				// Two services pin addresses: no single set to show.
				pinned = make([]Attachment, len(atts))
				for j, a := range atts {
					pinned[j] = Attachment{Network: a.Network}
				}
				break
			}
			pinned = atts
		}
	}
	if pinned != nil {
		return pinned
	}
	out := make([]Attachment, len(names))
	for i, n := range names {
		out[i] = Attachment{Network: n}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sameOrder reports whether two network lists are identical, order included:
// order decides which interface is eth0.
func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pinCount reports how many attachments carry an address or a MAC.
func pinCount(atts []Attachment) int {
	n := 0
	for _, a := range atts {
		if a.IP != "" || a.MAC != "" {
			n++
		}
	}
	return n
}

// serviceAttachments reads one service's `networks:` key in either form.
func serviceAttachments(n *yaml.Node) []Attachment {
	if n == nil {
		return nil
	}
	var out []Attachment
	switch n.Kind {
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if item.Kind == yaml.ScalarNode && item.Value != "" {
				out = append(out, Attachment{Network: item.Value})
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			a := Attachment{Network: n.Content[i].Value}
			if opts := n.Content[i+1]; opts != nil && opts.Kind == yaml.MappingNode {
				if v := mapGet(opts, "ipv4_address"); v != nil {
					a.IP = v.Value
				}
				if v := mapGet(opts, "mac_address"); v != nil {
					a.MAC = v.Value
				}
			}
			out = append(out, a)
		}
	}
	return out
}

// AttachedMAC reports the MAC pinned on the first network, empty when none.
func AttachedMAC(composeYAML string) string {
	if atts := AttachedNetworks(composeYAML); len(atts) > 0 {
		return atts[0].MAC
	}
	return ""
}

// AttachedNetwork reports the network a stack's services are on, and the fixed
// address if one is set -- the inverse of InjectNetwork, so a UI can show what
// is actually configured instead of defaulting to "none".
//
// Returns "" when the services declare no network, disagree about which one,
// or use network_mode (host/none/container:) rather than a named network:
// those are not something the network picker can represent, and claiming
// otherwise would let a save silently rewrite them.
// AttachedNetwork reports the first network a stack is on and the address
// pinned there. Kept for callers that only care about the primary.
func AttachedNetwork(composeYAML string) (network, ip string) {
	if atts := AttachedNetworks(composeYAML); len(atts) > 0 {
		return atts[0].Network, atts[0].IP
	}
	return "", ""
}

// serviceNetwork reads one service's `networks:` value, which compose allows
// as either a mapping ({net: {ipv4_address: x}}) or a plain list ([net]).
func serviceNetwork(n *yaml.Node) (name, ip string) {
	if n == nil {
		return "", ""
	}
	switch n.Kind {
	case yaml.MappingNode:
		if len(n.Content) < 2 {
			return "", ""
		}
		name = n.Content[0].Value
		if opts := n.Content[1]; opts != nil && opts.Kind == yaml.MappingNode {
			if a := mapGet(opts, "ipv4_address"); a != nil {
				ip = a.Value
			}
		}
	case yaml.SequenceNode:
		if len(n.Content) > 0 {
			name = n.Content[0].Value
		}
	}
	return name, ip
}

// Built-in network choices: not networks anyone creates, but the two states a
// stack can be in without one. Named as podman names them, so what fjord shows
// and what `podman inspect` reports are the same word.
const (
	// Bridge is podman's own NAT bridge -- the container gets a private
	// address and is reached on published ports. What a stack gets by asking
	// for nothing.
	Bridge = "bridge"
	// None is no network at all: on FreeBSD a jail with its own empty vnet,
	// so it has lo0 and no route anywhere.
	None = "none"
	// Host shares this host's stack: no address of its own, no port mapping,
	// a service binds the host's port directly.
	Host = "host"
)

// BuiltIn reports whether a name is one of the three states a stack can be in
// without a network of its own, rather than a network defined on the host.
//
// All three are listed on the Networks page and all three may be the default
// new installs land on: they are the same vocabulary everywhere, and a state
// that can be chosen per stack but never as the default is a distinction with
// no reason behind it that the reader can see.
func BuiltIn(name string) bool { return name == Bridge || name == None || name == Host }

// DisableNetwork puts a stack on no network at all.
//
// `network_mode: none` alone is not enough on FreeBSD: it leaves the jail
// without a vnet, which means it SHARES the host's stack -- it can reach the
// internet and bind host ports, the opposite of what the name suggests. The
// annotation gives it an empty vnet of its own, which is what actually
// isolates it (verified: lo0 only, no route out).
func DisableNetwork(composeYAML string) (string, error) { return setMode(composeYAML, None) }

// HostNetwork puts a stack on this host's own stack: it binds host ports
// directly, with no address and no mapping of its own. On FreeBSD that is a
// jail with no vnet -- which is why it needs no annotation, and why any vnet
// annotation DisableNetwork left behind has to come off (an empty vnet of its
// own is the exact opposite of sharing the host's).
func HostNetwork(composeYAML string) (string, error) { return setMode(composeYAML, Host) }

// setMode writes one network_mode across every service. Both modes take the
// same path: networks and network_mode are mutually exclusive, and published
// ports mean nothing in either -- "none" has nowhere to answer from and "host"
// is already on the host's ports -- so they are stashed, ready to come back
// when the stack is attached again.
func setMode(composeYAML, mode string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return "", fmt.Errorf("parse compose: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose is not a YAML mapping")
	}
	root := doc.Content[0]
	services := mapGet(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose has no services")
	}
	for i := 0; i+1 < len(services.Content); i += 2 {
		svc := services.Content[i+1]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		stashNetworks(svc)
		mapDelete(svc, "networks")
		mapDelete(svc, "mac_address")
		stashPorts(svc)
		clearMode(svc)
		mapSet(svc, "network_mode", scalar(mode))
		if mode != None {
			continue
		}
		ann := mapGet(svc, "annotations")
		if ann == nil || ann.Kind != yaml.MappingNode {
			ann = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			mapSet(svc, "annotations", ann)
		}
		if mapGet(ann, "org.freebsd.jail.vnet") == nil {
			ann.Content = append(ann.Content, scalar("org.freebsd.jail.vnet"), scalar("new"))
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	enc.Close()
	return buf.String(), nil
}

// clearMode takes a service back off whatever mode fjord put it on, including
// the vnet annotation that made "none" mean isolated. The annotations map goes
// too when nothing else is in it, so a round trip leaves no residue.
func clearMode(svc *yaml.Node) {
	mapDelete(svc, "network_mode")
	ann := mapGet(svc, "annotations")
	if ann == nil || ann.Kind != yaml.MappingNode {
		return
	}
	mapDelete(ann, "org.freebsd.jail.vnet")
	if len(ann.Content) == 0 {
		mapDelete(svc, "annotations")
	}
}

// NoNamedNetworks reports a stack that cannot be moved onto a named network: one
// on host mode with several services, whose parts reach each other over the
// shared stack. Bridge and none remain reachable -- only an address of its
// own breaks it. The UI asks so it can gray those options out rather than
// letting Save come back 400.
func NoNamedNetworks(composeYAML string) bool {
	if NetworkMode(composeYAML) != Host {
		return false
	}
	var doc yaml.Node
	if yaml.Unmarshal([]byte(composeYAML), &doc) != nil || len(doc.Content) == 0 {
		return false
	}
	services := mapGet(doc.Content[0], "services")
	return services != nil && services.Kind == yaml.MappingNode && len(services.Content) > 2
}

// NetworkMode reports the built-in mode a stack sits on -- "none", "host", or
// "" for neither. Only a mode every service agrees on counts: a stack where
// they disagree cannot be shown as one choice, and writing one back would move
// the others.
//
// This is the read side of the stack's network picker. AttachedNetworks
// returns nothing for a stack with a network_mode, which without this reads as
// "on the bridge, publishing ports" -- the exact opposite of "none".
func NetworkMode(composeYAML string) string {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(composeYAML), &doc) != nil || len(doc.Content) == 0 {
		return ""
	}
	services := mapGet(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode || len(services.Content) == 0 {
		return ""
	}
	mode := ""
	for i := 1; i < len(services.Content); i += 2 {
		m := mapGet(services.Content[i], "network_mode")
		if m == nil || (m.Value != None && m.Value != Host) {
			return ""
		}
		if mode == "" {
			mode = m.Value
		} else if mode != m.Value {
			return ""
		}
	}
	return mode
}
