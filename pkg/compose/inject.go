// Package compose provides minimal, structure-preserving edits to a stack's
// compose.yaml -- enough for fjord to wire up host provisioning (networks
// today) without taking over authorship of the file.
package compose

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

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
	// Service names the one service this attachment is for. Empty means every
	// service in the stack, which is what a single-service stack and every
	// caller before per-service networking sends.
	//
	// It is what lets a stack publish the service people use and keep the rest
	// on a private segment: immich-server on the LAN, its database and redis
	// on a network only it can reach.
	Service string `json:"service,omitempty"`
	IP      string `json:"ip,omitempty"`
	// IP6 is the IPv6 address for this attachment, on a network that has a v6
	// segment. Separate from IP rather than one field holding either: a
	// dual-stack attachment carries BOTH, and which family an address belongs
	// to is not something the network can be asked after the fact.
	IP6 string `json:"ip6,omitempty"`
	MAC string `json:"mac,omitempty"`
	// Iface is what the interface is called INSIDE the container, reported by
	// the daemon and never written to the compose. podman names them eth0,
	// eth1, ...; appjail names the jail side of the epair after the option
	// that made it, so the same row is sb_<name> on a host bridge and
	// eb_<name> on a virtual network. Showing "eth0" for a jail was simply
	// wrong -- `ifconfig eth0` there answers "interface eth0 does not exist".
	Iface string `json:"iface,omitempty"`
	// Own marks a network the compose declares itself (top-level networks:,
	// not external): compose makes it when the stack starts, under the
	// project's name, so it is no network of the host's to pick from. Read,
	// never written: InjectNetworks leaves a declared network's definition
	// as it stands.
	Own bool `json:"own,omitempty"`
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
	// Per-service as soon as any attachment names a service. Four services on
	// one private network is four attachments for the same network, which read
	// as a stack listing it four times -- "network \"lan\" is listed twice",
	// and nothing saved.
	perService := false
	for _, a := range atts {
		if a.Service != "" {
			perService = true
			break
		}
	}
	seen := map[string]bool{}
	pinned := false
	for _, a := range atts {
		if a.Network == "" {
			return "", fmt.Errorf("network name is required")
		}
		key := a.Network
		if perService {
			key = a.Service + "\x00" + a.Network
		}
		if seen[key] {
			if perService {
				return "", fmt.Errorf("%s is on network %q twice", a.Service, a.Network)
			}
			return "", fmt.Errorf("network %q is listed twice", a.Network)
		}
		seen[key] = true
		if a.MAC != "" && !macRe.MatchString(a.MAC) {
			return "", fmt.Errorf("invalid MAC address %q: want six hex pairs like 02:1a:2b:3c:4d:5e", a.MAC)
		}
		if a.IP != "" || a.IP6 != "" || a.MAC != "" {
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

	// Collect (name, valueIdx, node) for each service; service values sit at odd indices.
	type svc struct {
		name string
		idx  int
		node *yaml.Node
	}
	var svcs []svc
	for i := 0; i+1 < len(services.Content); i += 2 {
		svcs = append(svcs, svc{services.Content[i].Value, i + 1, deref(services.Content[i+1])})
	}
	if len(svcs) == 0 {
		return "", fmt.Errorf("compose has no services")
	}
	// A pinned address or MAC belongs to the service that serves: with several
	// services it can only go on the published one.
	target := svcs[0].name
	if pinned && !perService && len(svcs) > 1 {
		var published []string
		for _, sv := range svcs {
			if sv.node != nil && sv.node.Kind == yaml.MappingNode && hasPublishedPorts(sv.node) {
				published = append(published, sv.name)
			}
		}
		if len(published) != 1 {
			return "", fmt.Errorf("this stack has %d services and %d of them publish ports, so there is no single service to pin an address or MAC to -- attach without pinning either, or set ipv4_address/ipv6_address/mac_address yourself", len(svcs), len(published))
		}
		target = published[0]
	}

	own := ownNetworks(root)

	// Top-level networks: {<network>: {external: true}} (idempotent). The
	// built-in bridge is the project's own "default", which podman-compose
	// refuses to attach unless it is declared -- and it is not external.
	networks := mapEnsure(root, "networks")
	for _, a := range atts {
		if a.Network == Bridge {
			if mapGet(networks, composeDefault) == nil {
				mapSet(networks, composeDefault, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle})
			}
			continue
		}
		if own[a.Network] {
			continue
		}
		if mapGet(networks, a.Network) == nil {
			mapSet(networks, a.Network, externalNetworkNode())
		}
	}

	for _, s := range svcs {
		if s.node == nil || s.node.Kind != yaml.MappingNode {
			return "", fmt.Errorf("service %q is not a mapping", s.name)
		}
		use := atts
		keepOwn := own
		if perService {
			if use = attachmentsForService(atts, s.name); len(use) == 0 {
				continue // not named: left exactly as it is
			}
			keepOwn = nil
		} else if s.name != target {
			// Only the target carries pins; the others just join.
			use = make([]Attachment, len(atts))
			for i, a := range atts {
				use[i] = Attachment{Network: a.Network}
			}
		}
		svcNode := prepareServiceForWrite(root, services, s.idx)
		if err := moveService(svcNode, s.name, "", use, keepOwn); err != nil {
			return "", err
		}
	}
	ensureDefaultTopNetwork(root, services)

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

// deref follows YAML alias nodes to their target.
func deref(n *yaml.Node) *yaml.Node {
	for steps := 0; n != nil && n.Kind == yaml.AliasNode && steps < 32; steps++ {
		n = n.Alias
	}
	return n
}

func isMergeKey(k *yaml.Node) bool {
	return k != nil && (k.Value == "<<" || k.Tag == "!!merge")
}

func hasDirectMerge(m *yaml.Node) bool {
	if m == nil || m.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if isMergeKey(m.Content[i]) {
			return true
		}
	}
	return false
}

func hasAliasElem(s *yaml.Node) bool {
	if s == nil || s.Kind != yaml.SequenceNode {
		return false
	}
	for _, it := range s.Content {
		if it != nil && it.Kind == yaml.AliasNode {
			return true
		}
	}
	return false
}

// cloneResolved returns a deep copy of n with all YAML aliases dereferenced,
// anchors cleared, and << merge keys expanded into their mappings.
func cloneResolved(n *yaml.Node) *yaml.Node {
	return cloneResolvedSeen(n, map[*yaml.Node]bool{})
}

func cloneResolvedSeen(n *yaml.Node, active map[*yaml.Node]bool) *yaml.Node {
	n = deref(n)
	if n == nil {
		return nil
	}
	if active[n] {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	active[n] = true
	defer delete(active, n)

	cp := *n
	cp.Anchor = ""
	cp.Alias = nil
	if len(n.Content) > 0 {
		cp.Content = make([]*yaml.Node, len(n.Content))
		for i, c := range n.Content {
			cp.Content[i] = cloneResolvedSeen(c, active)
		}
	}
	if cp.Kind == yaml.MappingNode && hasDirectMerge(&cp) {
		inlineMerges(&cp, active)
	}
	return &cp
}

func inlineMerges(m *yaml.Node, active map[*yaml.Node]bool) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	if active == nil {
		active = map[*yaml.Node]bool{}
	}
	have := map[string]bool{}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !isMergeKey(m.Content[i]) {
			have[m.Content[i].Value] = true
		}
	}
	var out []*yaml.Node
	addFrom := func(src *yaml.Node) {
		src = cloneResolvedSeen(src, active)
		if src == nil || src.Kind != yaml.MappingNode {
			return
		}
		for j := 0; j+1 < len(src.Content); j += 2 {
			k, v := src.Content[j], src.Content[j+1]
			if !have[k.Value] {
				have[k.Value] = true
				out = append(out, k, v)
			}
		}
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !isMergeKey(m.Content[i]) {
			out = append(out, m.Content[i], m.Content[i+1])
			continue
		}
		mv := deref(m.Content[i+1])
		if mv == nil {
			continue
		}
		switch mv.Kind {
		case yaml.MappingNode:
			addFrom(mv)
		case yaml.SequenceNode:
			for _, item := range mv.Content {
				addFrom(item)
			}
		}
	}
	m.Content = out
}

func mapGetSeen(m *yaml.Node, key string, seen map[*yaml.Node]bool) *yaml.Node {
	m = deref(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !isMergeKey(m.Content[i]) && m.Content[i].Value == key {
			return deref(m.Content[i+1])
		}
	}
	return lookupMerged(m, key, seen)
}

func lookupMerged(m *yaml.Node, key string, seen map[*yaml.Node]bool) *yaml.Node {
	m = deref(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	if seen == nil {
		seen = map[*yaml.Node]bool{}
	}
	if seen[m] {
		return nil
	}
	seen[m] = true
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !isMergeKey(m.Content[i]) {
			continue
		}
		mv := deref(m.Content[i+1])
		if mv == nil {
			continue
		}
		switch mv.Kind {
		case yaml.MappingNode:
			if v := mapGetSeen(mv, key, seen); v != nil {
				return v
			}
		case yaml.SequenceNode:
			for _, item := range mv.Content {
				if v := mapGetSeen(deref(item), key, seen); v != nil {
					return v
				}
			}
		}
	}
	return nil
}

func unshareSubtree(root, sub *yaml.Node) {
	if root == nil || sub == nil {
		return
	}
	subSet := map[*yaml.Node]bool{}
	collectNodes(sub, subSet)
	detachSubtreeRefs(root, subSet, map[*yaml.Node]bool{})
}

func collectNodes(n *yaml.Node, out map[*yaml.Node]bool) {
	if n == nil || out[n] {
		return
	}
	out[n] = true
	for _, c := range n.Content {
		collectNodes(c, out)
	}
}

func detachSubtreeRefs(n *yaml.Node, subSet, seen map[*yaml.Node]bool) {
	if n == nil || seen[n] || subSet[n] {
		return
	}
	seen[n] = true
	if n.Kind == yaml.MappingNode && hasMergeInto(n, subSet) {
		inlineMerges(n, nil)
	}
	for i, c := range n.Content {
		if c != nil && c.Kind == yaml.AliasNode && subSet[deref(c)] {
			n.Content[i] = cloneResolved(c)
			continue
		}
		detachSubtreeRefs(c, subSet, seen)
	}
}

func hasMergeInto(m *yaml.Node, subSet map[*yaml.Node]bool) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !isMergeKey(m.Content[i]) {
			continue
		}
		mv := deref(m.Content[i+1])
		if mv == nil {
			continue
		}
		if subSet[mv] {
			return true
		}
		if mv.Kind == yaml.SequenceNode {
			for _, item := range mv.Content {
				if subSet[deref(item)] {
					return true
				}
			}
		}
	}
	return false
}

func prepareServiceForWrite(root, services *yaml.Node, valueIdx int) *yaml.Node {
	svc := services.Content[valueIdx]
	if svc == nil {
		return nil
	}
	unshareSubtree(root, svc)
	svc = cloneResolved(svc)
	services.Content[valueIdx] = svc
	return svc
}

func isEmptyMappingNode(n *yaml.Node) bool {
	n = deref(n)
	return n == nil || (n.Kind == yaml.MappingNode && len(n.Content) == 0)
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	return mapGetSeen(m, key, nil)
}

func mapSet(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !isMergeKey(m.Content[i]) && m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, scalar(key), val)
}

// mapEnsure returns the mapping node at key, creating an empty one if absent.
func mapEnsure(m *yaml.Node, key string) *yaml.Node {
	m = deref(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !isMergeKey(m.Content[i]) && m.Content[i].Value == key {
			v := m.Content[i+1]
			if v != nil && (v.Kind == yaml.AliasNode || hasDirectMerge(v) || hasAliasElem(v)) {
				v = cloneResolved(v)
				m.Content[i+1] = v
			}
			if v == nil {
				v = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				m.Content[i+1] = v
			} else if v.Kind != yaml.MappingNode {
				v.Kind, v.Tag, v.Value, v.Style = yaml.MappingNode, "!!map", "", 0
			}
			return v
		}
	}
	if v := lookupMerged(m, key, nil); v != nil {
		c := cloneResolved(v)
		if c.Kind != yaml.MappingNode {
			c.Kind, c.Tag, c.Value, c.Style = yaml.MappingNode, "!!map", "", 0
		}
		m.Content = append(m.Content, scalar(key), c)
		return c
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

func hasPublishedPorts(svc *yaml.Node) bool {
	return mapGet(svc, "ports") != nil || mapGet(svc, "x-fjord-published") != nil
}

func mergePortNodes(a, b *yaml.Node) *yaml.Node {
	if a == nil || a.Kind != yaml.SequenceNode {
		return b
	}
	if b == nil || b.Kind != yaml.SequenceNode {
		return a
	}
	out := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	seen := map[string]bool{}
	for _, src := range []*yaml.Node{a, b} {
		for _, it := range src.Content {
			if it.Kind == yaml.ScalarNode {
				if seen[it.Value] {
					continue
				}
				seen[it.Value] = true
			}
			out.Content = append(out.Content, it)
		}
	}
	return out
}

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
	if prev := mapGet(svc, "x-fjord-published"); prev != nil {
		mapSet(svc, "x-fjord-published", mergePortNodes(prev, ports))
	} else {
		mapSet(svc, "x-fjord-published", ports)
	}
	mapDelete(svc, "ports")
}

func restorePorts(svc *yaml.Node) bool {
	stashed := mapGet(svc, "x-fjord-published")
	if stashed == nil {
		return false
	}
	if live := mapGet(svc, "ports"); live != nil {
		mapSet(svc, "ports", mergePortNodes(stashed, live))
	} else {
		mapSet(svc, "ports", stashed)
	}
	mapDelete(svc, "x-fjord-published")
	return true
}

// stashNetworks keeps a service's attachments where putting it on a mode would
// otherwise throw them away. Same contract as stashPorts, and the same guard
// for the same reason: lan,lan2 -> none -> host must not let the second
// transition overwrite the real stash with the empty one it finds.
//
// The stash is normalized to a mapping of network -> pins rather than a copy
// of whatever `networks:` held, so the service-level mac_address (the old
// single-network form, and the one a DHCP reservation is keyed on) has
// somewhere to go. Without that it is the most expensive thing on the page to
// lose, and it would vanish silently.
func stashNetworks(svc *yaml.Node) {
	stashSelectedNetworks(svc, nil)
}

func stashSelectedNetworks(svc *yaml.Node, skip map[string]bool) {
	raw := serviceAttachments(mapGet(svc, "networks"))
	var atts []Attachment
	for _, a := range raw {
		if !skip[a.Network] {
			atts = append(atts, a)
		}
	}
	if len(atts) == 0 {
		return
	}
	byNetAliases := serviceAliases(svc)
	if len(atts) == 1 && atts[0].Network == Bridge &&
		atts[0].IP == "" && atts[0].IP6 == "" && atts[0].MAC == "" &&
		mapGet(svc, "mac_address") == nil && len(byNetAliases[Bridge]) == 0 {
		return
	}
	if existing := mapGet(svc, "x-fjord-networks"); existing != nil && existing.Kind == yaml.MappingNode {
		for _, a := range atts {
			if mapGet(existing, a.Network) == nil {
				existing.Content = append(existing.Content, scalar(a.Network), networkPinsNode(a, byNetAliases[a.Network]))
			}
		}
		return
	}
	if atts[0].MAC == "" {
		if m := mapGet(svc, "mac_address"); m != nil {
			atts[0].MAC = m.Value
		}
	}
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, a := range atts {
		out.Content = append(out.Content, scalar(a.Network), networkPinsNode(a, byNetAliases[a.Network]))
	}
	mapSet(svc, "x-fjord-networks", out)
}

func networkPinsNode(a Attachment, aliases []string) *yaml.Node {
	pins := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if a.IP != "" {
		pins.Content = append(pins.Content, scalar("ipv4_address"), scalar(a.IP))
	}
	if a.IP6 != "" {
		pins.Content = append(pins.Content, scalar("ipv6_address"), scalar(a.IP6))
	}
	if a.MAC != "" {
		pins.Content = append(pins.Content, scalar("mac_address"), scalar(a.MAC))
	}
	if len(aliases) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, al := range aliases {
			seq.Content = append(seq.Content, scalar(al))
		}
		pins.Content = append(pins.Content, scalar("aliases"), seq)
	}
	return pins
}

func serviceAliases(svc *yaml.Node) map[string][]string {
	byNet := map[string][]string{}
	for _, key := range []string{"networks", "x-fjord-networks"} {
		n := mapGet(svc, key)
		if n == nil || n.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			netName := fjordName(n.Content[i].Value)
			opts := n.Content[i+1]
			if opts == nil || opts.Kind != yaml.MappingNode {
				continue
			}
			al := mapGet(opts, "aliases")
			if al == nil || al.Kind != yaml.SequenceNode {
				continue
			}
			var list []string
			for _, it := range al.Content {
				if it.Kind == yaml.ScalarNode && it.Value != "" {
					list = append(list, it.Value)
				}
			}
			if len(list) == 0 {
				continue
			}
			if len(byNet[netName]) == 0 {
				byNet[netName] = list
			}
		}
	}
	return byNet
}

func matchAliases(atts []Attachment, byNet map[string][]string) map[string][]string {
	if len(atts) == 0 || len(byNet) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, a := range atts {
		if als := byNet[a.Network]; len(als) > 0 {
			out[a.Network] = als
		}
	}
	return out
}

// StashedNetworks reports the attachments a stack had before it was put on a
// mode, so the UI can offer them back rather than making the user retype an
// address set it still has on disk.
func StashedNetworks(composeYAML string) []Attachment {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(composeYAML), &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	services := mapGet(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil
	}
	own := ownNetworks(root)
	// The first service that has one: they are written together and read back
	// as one set, the same as AttachedNetworks reports one set for the stack.
	for i := 1; i < len(services.Content); i += 2 {
		raw := serviceAttachments(mapGet(services.Content[i], "x-fjord-networks"))
		var atts []Attachment
		for _, a := range raw {
			if a.Network == Bridge && a.IP == "" && a.IP6 == "" && a.MAC == "" {
				continue
			}
			if !own[a.Network] {
				atts = append(atts, a)
			}
		}
		if len(atts) > 0 {
			return atts
		}
	}
	return nil
}

// mapDelete removes a key and its value from a mapping node.
func mapDelete(m *yaml.Node, key string) {
	m = deref(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	if lookupMerged(m, key, nil) != nil {
		inlineMerges(m, nil)
	}
	kept := m.Content[:0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			continue
		}
		kept = append(kept, m.Content[i], m.Content[i+1])
	}
	m.Content = kept
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

func unnameSelf(svc *yaml.Node, name string) {
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

func hasSharedNetworkMode(svc *yaml.Node) (string, bool) {
	m := mapGet(svc, "network_mode")
	if m == nil {
		return "", false
	}
	if strings.HasPrefix(m.Value, "service:") || strings.HasPrefix(m.Value, "container:") {
		return m.Value, true
	}
	return "", false
}

// moveService transitions one service node to the requested network state:
// either a list of network attachments (atts non-empty) or a built-in mode
// (Bridge, None, Host).
func moveService(svc *yaml.Node, name, mode string, atts []Attachment, own map[string]bool) error {
	if sharedMode, shared := hasSharedNetworkMode(svc); shared {
		if len(atts) > 0 {
			return fmt.Errorf("service %q uses network_mode: %s, which fjord does not know how to move onto a network", name, sharedMode)
		}
		return nil
	}

	byNetAliases := serviceAliases(svc)

	if len(atts) > 0 {
		clearMode(svc)
		use := make([]Attachment, len(atts))
		copy(use, atts)
		have := map[string]bool{}
		for _, a := range use {
			have[a.Network] = true
		}
		for _, key := range []string{"networks", "x-fjord-networks"} {
			for _, e := range serviceAttachments(mapGet(svc, key)) {
				if own[e.Network] && !have[e.Network] {
					have[e.Network] = true
					use = append(use, e)
				}
			}
		}
		netAliases := matchAliases(use, byNetAliases)
		if bridgeAliases := byNetAliases[Bridge]; len(bridgeAliases) > 0 && !have[Bridge] {
			st := mapEnsure(svc, "x-fjord-networks")
			mapSet(st, Bridge, networkPinsNode(Attachment{Network: Bridge}, bridgeAliases))
		} else {
			mapDelete(svc, "x-fjord-networks")
		}
		mapSet(svc, "networks", serviceNetworksNodeWithAliases(use, netAliases))
		mapDelete(svc, "mac_address")
		nameSelf(svc, name)
		stashPorts(svc)
		return nil
	}

	if mode == Bridge || mode == "" {
		var ownAtts []Attachment
		haveOwn := map[string]bool{}
		for _, key := range []string{"networks", "x-fjord-networks"} {
			for _, e := range serviceAttachments(mapGet(svc, key)) {
				if own[e.Network] && !haveOwn[e.Network] {
					haveOwn[e.Network] = true
					ownAtts = append(ownAtts, e)
				}
			}
		}
		stashSelectedNetworks(svc, own)
		if st := mapGet(svc, "x-fjord-networks"); st != nil && st.Kind == yaml.MappingNode && len(own) > 0 {
			for o := range own {
				mapDelete(st, o)
			}
			if len(st.Content) == 0 {
				mapDelete(svc, "x-fjord-networks")
			}
		}
		if len(ownAtts) > 0 {
			netAliases := matchAliases(ownAtts, byNetAliases)
			mapSet(svc, "networks", serviceNetworksNodeWithAliases(ownAtts, netAliases))
		} else if bridgeAliases := byNetAliases[Bridge]; len(bridgeAliases) > 0 {
			mapSet(svc, "networks", serviceNetworksNodeWithAliases(
				[]Attachment{{Network: Bridge}},
				map[string][]string{Bridge: bridgeAliases},
			))
			if st := mapGet(svc, "x-fjord-networks"); st != nil && st.Kind == yaml.MappingNode {
				mapDelete(st, composeDefault)
				mapDelete(st, Bridge)
				if len(st.Content) == 0 {
					mapDelete(svc, "x-fjord-networks")
				}
			}
		} else {
			mapDelete(svc, "networks")
		}
		mapDelete(svc, "mac_address")
		clearMode(svc)
		restorePorts(svc)
		unnameSelf(svc, name)
		return nil
	}

	stashNetworks(svc)
	mapDelete(svc, "networks")
	mapDelete(svc, "mac_address")
	stashPorts(svc)
	clearMode(svc)
	unnameSelf(svc, name)
	mapSet(svc, "network_mode", scalar(mode))
	if mode == None {
		ann := mapGet(svc, "annotations")
		if ann == nil || ann.Kind != yaml.MappingNode {
			ann = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			mapSet(svc, "annotations", ann)
		}
		if mapGet(ann, "org.freebsd.jail.vnet") == nil {
			ann.Content = append(ann.Content, scalar("org.freebsd.jail.vnet"), scalar("new"))
		}
	}
	return nil
}

// serviceNetworksNode renders the service-level `networks:` key. A plain list
// when nothing is pinned -- the form most compose files already use -- and a
// mapping as soon as any entry carries an address or a MAC.
func serviceNetworksNode(atts []Attachment) *yaml.Node {
	return serviceNetworksNodeWithAliases(atts, nil)
}

func serviceNetworksNodeWithAliases(atts []Attachment, aliases map[string][]string) *yaml.Node {
	plain := len(aliases) == 0
	for _, a := range atts {
		if a.IP != "" || a.IP6 != "" || a.MAC != "" {
			plain = false
		}
	}
	if plain {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, a := range atts {
			seq.Content = append(seq.Content, scalar(composeName(a.Network)))
		}
		return seq
	}
	outer := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, a := range atts {
		inner := networkPinsNode(a, aliases[a.Network])
		if len(inner.Content) == 0 {
			inner.Style = yaml.FlowStyle // "net: {}" -- an empty mapping, not null
		}
		outer.Content = append(outer.Content, scalar(composeName(a.Network)), inner)
	}
	return outer
}

// composeDefault is compose's name for the project's own network -- what fjord
// calls the built-in bridge. A service listing it next to other networks keeps
// the bridge (and its published ports) and gains the others.
const composeDefault = "default"

func composeName(network string) string {
	if network == Bridge {
		return composeDefault
	}
	return network
}

func fjordName(network string) string {
	if network == composeDefault {
		return Bridge
	}
	return network
}

// macRe bounds a MAC address: six colon- or dash-separated hex pairs. It ends
// up in a compose file and on an ifconfig command line.
var macRe = regexp.MustCompile(`^([0-9a-fA-F]{2}[:-]){5}[0-9a-fA-F]{2}$`)

func anyServiceUsesNetwork(services *yaml.Node, netName string) bool {
	if services == nil || services.Kind != yaml.MappingNode {
		return false
	}
	target := fjordName(netName)
	for i := 1; i < len(services.Content); i += 2 {
		svc := services.Content[i]
		if svc == nil || svc.Kind != yaml.MappingNode {
			continue
		}
		for _, a := range serviceAttachments(mapGet(svc, "networks")) {
			if a.Network == target {
				return true
			}
		}
	}
	return false
}

func ensureDefaultTopNetwork(root, services *yaml.Node) {
	if !anyServiceUsesNetwork(services, composeDefault) {
		return
	}
	networks := mapEnsure(root, "networks")
	if mapGet(networks, composeDefault) == nil {
		mapSet(networks, composeDefault, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle})
	}
}

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

	own := ownNetworks(root)
	for i := 0; i+1 < len(services.Content); i += 2 {
		name, svc := services.Content[i].Value, deref(services.Content[i+1])
		if svc == nil || svc.Kind != yaml.MappingNode {
			continue
		}
		svc = prepareServiceForWrite(root, services, i+1)
		if err := moveService(svc, name, Bridge, nil, own); err != nil {
			return "", err
		}
	}

	// Top-level declarations of networks nothing joins any more. Only the
	// external ones -- an internal network is part of the stack's own design.
	defaultUsed := anyServiceUsesNetwork(services, composeDefault)
	if nets := mapGet(root, "networks"); nets != nil && nets.Kind == yaml.MappingNode {
		kept := nets.Content[:0]
		for i := 0; i+1 < len(nets.Content); i += 2 {
			name := nets.Content[i].Value
			v := nets.Content[i+1]
			if name == composeDefault && !defaultUsed && isEmptyMappingNode(v) {
				continue
			}
			if v == nil || v.Kind != yaml.MappingNode || mapGet(v, "external") == nil {
				kept = append(kept, nets.Content[i], nets.Content[i+1])
			}
		}
		nets.Content = kept
		if len(nets.Content) == 0 {
			mapDelete(root, "networks")
		}
	}
	ensureDefaultTopNetwork(root, services)

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
	declared := ownNetworks(root)
	var names []string
	var pinned []Attachment
	first := true
	for i := 0; i+1 < len(services.Content); i += 2 {
		svc := services.Content[i+1]
		if svc.Kind != yaml.MappingNode || mapGet(svc, "network_mode") != nil {
			return nil // host/none/container: -- not a named network
		}
		atts := markOwn(serviceAttachments(mapGet(svc, "networks")), declared)
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
	return markOwn(out, declared)
}

// ownNetworks names the networks the compose defines itself: a top-level
// networks: entry that is not external. Compose's "default" is the bridge.
func ownNetworks(root *yaml.Node) map[string]bool {
	out := map[string]bool{}
	nets := mapGet(root, "networks")
	if nets == nil || nets.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(nets.Content); i += 2 {
		name := fjordName(nets.Content[i].Value)
		if name == Bridge {
			continue
		}
		if v := nets.Content[i+1]; v != nil && v.Kind == yaml.MappingNode {
			if ext := mapGet(v, "external"); ext != nil && ext.Value != "false" {
				continue
			}
		}
		out[name] = true
	}
	return out
}

func markOwn(atts []Attachment, declared map[string]bool) []Attachment {
	for i := range atts {
		atts[i].Own = declared[atts[i].Network]
	}
	return atts
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
		if a.IP != "" || a.IP6 != "" || a.MAC != "" {
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
				out = append(out, Attachment{Network: fjordName(item.Value)})
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			a := Attachment{Network: fjordName(n.Content[i].Value)}
			if opts := n.Content[i+1]; opts != nil && opts.Kind == yaml.MappingNode {
				if v := mapGet(opts, "ipv4_address"); v != nil {
					a.IP = v.Value
				}
				if v := mapGet(opts, "ipv6_address"); v != nil {
					a.IP6 = v.Value
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
	own := ownNetworks(root)
	for i := 0; i+1 < len(services.Content); i += 2 {
		name, svc := services.Content[i].Value, deref(services.Content[i+1])
		if svc == nil || svc.Kind != yaml.MappingNode {
			continue
		}
		svc = prepareServiceForWrite(root, services, i+1)
		if err := moveService(svc, name, mode, nil, own); err != nil {
			return "", err
		}
	}
	ensureDefaultTopNetwork(root, services)
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
	if _, shared := hasSharedNetworkMode(svc); !shared {
		mapDelete(svc, "network_mode")
	}
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

// ServiceAttachments reports each service's networks by service name.
//
// AttachedNetworks answers for the stack and gives up the moment two services
// disagree -- "the table cannot represent it" was true of a table with one row
// per network. This makes the disagreement the answer instead: a stack whose
// app is on the LAN and whose database is on a private segment is describable,
// which is the whole point of putting them on different ones.
func ServiceAttachments(composeYAML string) map[string][]Attachment {
	out := map[string][]Attachment{}
	var doc yaml.Node
	if yaml.Unmarshal([]byte(composeYAML), &doc) != nil || len(doc.Content) == 0 {
		return out
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return out
	}
	services := mapGet(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return out
	}
	declared := ownNetworks(root)
	for i := 0; i+1 < len(services.Content); i += 2 {
		name, svc := services.Content[i].Value, services.Content[i+1]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		atts := markOwn(serviceAttachments(mapGet(svc, "networks")), declared)
		for j := range atts {
			atts[j].Service = name
		}
		if len(atts) > 0 {
			out[name] = atts
		}
	}
	return out
}

// attachmentsForService is the attachments that apply to one service: those
// naming it, plus any naming none at all, which mean every service.
func attachmentsForService(atts []Attachment, service string) []Attachment {
	var out []Attachment
	for _, a := range atts {
		if a.Service == "" || a.Service == service {
			a.Service = ""
			out = append(out, a)
		}
	}
	return out
}

// SeedPublished notes port as what service publishes once it is off the
// host's network, when nothing says so yet: a host-networked app has no
// ports entry (its port is a host port already), so a move to the bridge
// or a LAN segment had nothing to bring back. Stashed, not published:
// on the host's network it stays as it ships.
func SeedPublished(composeYAML, service, port string) (string, error) {
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
		return composeYAML, nil
	}
	var svc *yaml.Node
	var svcIdx int
	for i := 0; i+1 < len(services.Content); i += 2 {
		if services.Content[i].Value == service {
			svc = deref(services.Content[i+1])
			svcIdx = i + 1
			break
		}
	}
	if svc == nil || svc.Kind != yaml.MappingNode || hasPublishedPorts(svc) {
		return composeYAML, nil
	}
	svc = prepareServiceForWrite(root, services, svcIdx)
	mapSet(svc, "x-fjord-published", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{scalar(port + ":" + port)}})
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	enc.Close()
	return buf.String(), nil
}

// SetServiceModes puts named services on a built-in mode, one service at a
// time, leaving every other service exactly as it is.
//
// setMode answers for the whole stack, which is the right shape for a
// single-service app and the wrong one for a stack whose parts differ: immich
// wants its server on the host's stack and its database on a private segment,
// and saying that needs a per-service answer.
//
// Bridge means "the engine's own default", which in compose is the absence of
// both network_mode and networks -- so it clears rather than writes.
func SetServiceModes(composeYAML string, modes map[string]string) (string, error) {
	if len(modes) == 0 {
		return composeYAML, nil
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
	own := ownNetworks(root)
	for i := 0; i+1 < len(services.Content); i += 2 {
		name, svc := services.Content[i].Value, deref(services.Content[i+1])
		mode, want := modes[name]
		if !want || svc == nil || svc.Kind != yaml.MappingNode {
			continue
		}
		svc = prepareServiceForWrite(root, services, i+1)
		if err := moveService(svc, name, mode, nil, own); err != nil {
			return "", err
		}
	}
	ensureDefaultTopNetwork(root, services)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	enc.Close()
	return buf.String(), nil
}

// RepublishPorts gives back the published ports of the named services.
//
// Attaching stashes them because a container with its own address on a real
// segment IS the endpoint -- publishing to the host as well is redundant, and
// on several stacks it is a port conflict. That reasoning does not hold for a
// segment nothing off this host can reach: there the container's address is
// useless to a browser, and without its ports the stack answers nowhere at
// all. immich on its own private segment came up healthy and could not be
// opened from anywhere but the host itself.
//
// Which services those are is the caller's to decide -- only it knows which
// networks are private -- and the stash is what makes giving them back exact
// rather than a guess at what the ports used to be.
func RepublishPorts(composeYAML string, services []string) (string, error) {
	if len(services) == 0 {
		return composeYAML, nil
	}
	want := map[string]bool{}
	for _, s := range services {
		want[s] = true
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return "", fmt.Errorf("parse compose: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose is not a YAML mapping")
	}
	root := doc.Content[0]
	svcs := mapGet(root, "services")
	if svcs == nil || svcs.Kind != yaml.MappingNode {
		return composeYAML, nil
	}
	changed := false
	for i := 0; i+1 < len(svcs.Content); i += 2 {
		name, svc := svcs.Content[i].Value, deref(svcs.Content[i+1])
		if !want[name] || svc == nil || svc.Kind != yaml.MappingNode {
			continue
		}
		if mapGet(svc, "x-fjord-published") == nil {
			continue
		}
		svc = prepareServiceForWrite(root, svcs, i+1)
		if restorePorts(svc) {
			changed = true
		}
	}
	if !changed {
		return composeYAML, nil
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

// PinAddress sets service's ipv4_address on network, leaving the rest of the
// compose as written. A list entry (`networks: [lan]`) becomes the map form
// that can carry the address; an address already pinned is not changed.
func PinAddress(composeYAML, service, network, ip string) (string, error) {
	return pinOnNetwork(composeYAML, service, network, "ipv4_address", ip)
}

// PinMAC is PinAddress for the MAC: what a DHCP network's lease follows.
func PinMAC(composeYAML, service, network, mac string) (string, error) {
	return pinOnNetwork(composeYAML, service, network, "mac_address", mac)
}

// pinOnNetwork sets key on one service's entry for one network, unless the
// operator already set it.
func pinOnNetwork(composeYAML, service, network, key, value string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return "", fmt.Errorf("parse compose: %w", err)
	}
	if len(doc.Content) == 0 {
		return "", fmt.Errorf("empty compose")
	}
	root := doc.Content[0]
	services := mapGet(root, "services")
	if services == nil { // mapGet does not take a nil node
		return "", fmt.Errorf("compose has no services")
	}
	var svc *yaml.Node
	var svcIdx int
	for i := 0; i+1 < len(services.Content); i += 2 {
		if services.Content[i].Value == service {
			svc = deref(services.Content[i+1])
			svcIdx = i + 1
			break
		}
	}
	if svc == nil {
		return "", fmt.Errorf("no service %q", service)
	}
	svc = prepareServiceForWrite(root, services, svcIdx)
	nets := mapGet(svc, "networks")
	if nets == nil {
		return "", fmt.Errorf("%s is on no named network", service)
	}
	if nets.Kind == yaml.SequenceNode {
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, it := range nets.Content {
			m.Content = append(m.Content, scalar(it.Value), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
		}
		mapSet(svc, "networks", m)
		nets = m
	}
	entry := mapGet(nets, network)
	if entry == nil {
		return "", fmt.Errorf("%s is not on %s", service, network)
	}
	if entry.Kind != yaml.MappingNode { // `lan:` with no value
		entry.Kind, entry.Tag, entry.Value = yaml.MappingNode, "!!map", ""
	}
	if mapGet(entry, key) != nil {
		return composeYAML, nil
	}
	entry.Content = append(entry.Content, scalar(key), scalar(value))
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	enc.Close()
	return buf.String(), nil
}
