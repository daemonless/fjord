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
func InjectNetwork(composeYAML, network, ip, mac string) (string, error) {
	if network == "" {
		return "", fmt.Errorf("network name is required")
	}
	if mac != "" && !macRe.MatchString(mac) {
		return "", fmt.Errorf("invalid MAC address %q: want six hex pairs like 02:1a:2b:3c:4d:5e", mac)
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
		if sv.node.Kind == yaml.MappingNode && mapGet(sv.node, "network_mode") != nil {
			mode := mapGet(sv.node, "network_mode").Value
			return "", fmt.Errorf("service %q uses network_mode: %s, so this stack cannot take its own address -- its services reach each other over localhost", sv.name, mode)
		}
	}
	// A fixed address belongs to the service that serves: with several
	// services the address can only be pinned to the published one.
	target := svcs[0].name
	if (ip != "" || mac != "") && len(svcs) > 1 {
		var published []string
		for _, sv := range svcs {
			if sv.node.Kind == yaml.MappingNode && mapGet(sv.node, "ports") != nil {
				published = append(published, sv.name)
			}
		}
		if len(published) != 1 {
			what, kind, key := ip, "a fixed IP", "ipv4_address"
			if what == "" {
				what, kind, key = mac, "a fixed MAC", "mac_address"
			}
			return "", fmt.Errorf("this stack has %d services and %d of them publish ports, so there is no single service to give %s to -- attach without %s, or set %s yourself", len(svcs), len(published), what, kind, key)
		}
		target = published[0]
	}

	// Top-level networks: {<network>: {external: true}} (idempotent).
	networks := mapEnsure(root, "networks")
	if mapGet(networks, network) == nil {
		mapSet(networks, network, externalNetworkNode())
	}

	for _, s := range svcs {
		if s.node.Kind != yaml.MappingNode {
			return "", fmt.Errorf("service %q is not a mapping", s.name)
		}
		// Attaching has to be repeatable: changing the address or the MAC, or
		// moving the stack to another network, all come back through here with
		// the service already attached. Only a service on SEVERAL networks is
		// refused -- that is a compose its author wrote, and replacing the key
		// would drop a network fjord never added.
		if n := mapGet(s.node, "networks"); n != nil && declaredNetworks(n) > 1 {
			return "", fmt.Errorf("service %q declares several networks; fjord will not replace them -- edit the compose directly", s.name)
		}
		svcIP := ""
		if s.name == target {
			svcIP = ip // only the published service gets the fixed address
			// The MAC belongs with it: a DHCP reservation is keyed on the
			// MAC, so it has to land on the service that holds the address.
			// Clearing the field means no pinned MAC, so drop a stale one.
			if mac != "" {
				mapSet(s.node, "mac_address", scalar(mac))
			} else {
				mapDelete(s.node, "mac_address")
			}
		} else {
			mapDelete(s.node, "mac_address")
		}
		mapSet(s.node, "networks", serviceNetworkNode(network, svcIP))
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
func AttachedMAC(composeYAML string) string {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil || len(doc.Content) == 0 {
		return ""
	}
	services := mapGet(doc.Content[0], "services")
	if services == nil {
		return ""
	}
	for i := 1; i < len(services.Content); i += 2 {
		if m := mapGet(services.Content[i], "mac_address"); m != nil {
			return m.Value
		}
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
func AttachedNetwork(composeYAML string) (network, ip string) {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(composeYAML), &doc) != nil || len(doc.Content) == 0 {
		return "", ""
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return "", ""
	}
	services := mapGet(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return "", ""
	}
	first := true
	for i := 0; i+1 < len(services.Content); i += 2 {
		svc := services.Content[i+1]
		if svc.Kind != yaml.MappingNode {
			return "", ""
		}
		if mapGet(svc, "network_mode") != nil {
			return "", "" // host/none/container: -- not a named network
		}
		name, addr := serviceNetwork(mapGet(svc, "networks"))
		if first {
			network, ip, first = name, addr, false
			continue
		}
		if name != network {
			return "", "" // services disagree; the picker cannot represent it
		}
		if addr != ip {
			ip = "" // same network, different addresses: no single IP to show
		}
	}
	return network, ip
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
