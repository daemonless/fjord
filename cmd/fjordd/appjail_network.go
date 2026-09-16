package main

import (
	"fmt"
	"strings"

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
func epairName(stackID string) string {
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
	if len(name) > ifaceMax {
		name = name[:ifaceMax]
	}
	// An interface name may not start with a digit on FreeBSD.
	if name[0] >= '0' && name[0] <= '9' {
		name = "j" + name
		if len(name) > ifaceMax {
			name = name[:ifaceMax]
		}
	}
	return name
}

// setDirectorNetwork rewrites a director document's top-level options so the
// project's jails sit on a host bridge with a fixed address, replacing
// appjail's default NAT virtualnet.
//
// It also drops every service's `expose:`. appjail refuses that outright
// alongside a bridge ("expose requires the following options: virtualnet"),
// and rightly so: a jail on its own address has no host port to forward.
func setDirectorNetwork(directorYML, stackID, network, ip string) (string, error) {
	net, ok := hostnet.Get(network)
	if !ok {
		return "", fmt.Errorf("no network named %q is defined on this host", network)
	}
	if net.Bridge == "" || net.Subnet == "" {
		return "", fmt.Errorf("network %q has no bridge or subnet to place a jail on", network)
	}
	prefix := "24"
	if _, p, found := strings.Cut(net.Subnet, "/"); found {
		prefix = p
	}
	if ip == "" {
		return "", fmt.Errorf("an address is required to place a jail on %q: appjail has no address pool for a network it does not manage", network)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(directorYML), &doc); err != nil {
		return "", fmt.Errorf("parse director: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("director is not a YAML mapping")
	}
	root := doc.Content[0]

	iface := epairName(stackID)
	opts := &yaml.Node{Kind: yaml.SequenceNode}
	for _, kv := range [][2]string{
		{"bridge", fmt.Sprintf("epair:%s bridge:%s", iface, net.Bridge)},
		{"ifconfig", fmt.Sprintf("sb_%s:%s/%s", iface, ip, prefix)},
		{"defaultrouter", net.Gateway},
	} {
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
