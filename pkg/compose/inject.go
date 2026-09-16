// Package compose provides minimal, structure-preserving edits to a stack's
// compose.yaml -- enough for fjord to wire up host provisioning (networks
// today) without taking over authorship of the file.
package compose

import (
	"bytes"
	"fmt"

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
func InjectNetwork(composeYAML, network, ip string) (string, error) {
	if network == "" {
		return "", fmt.Errorf("network name is required")
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
	if ip != "" && len(svcs) > 1 {
		var published []string
		for _, sv := range svcs {
			if sv.node.Kind == yaml.MappingNode && mapGet(sv.node, "ports") != nil {
				published = append(published, sv.name)
			}
		}
		if len(published) != 1 {
			return "", fmt.Errorf("this stack has %d services and %d of them publish ports, so there is no single service to give %s to -- attach without a fixed IP, or set ipv4_address yourself", len(svcs), len(published), ip)
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
		if mapGet(s.node, "networks") != nil {
			return "", fmt.Errorf("service %q already declares networks; remove it to auto-attach", s.name)
		}
		svcIP := ""
		if s.name == target {
			svcIP = ip // only the published service gets the fixed address
		}
		mapSet(s.node, "networks", serviceNetworkNode(network, svcIP))
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
