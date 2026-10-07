package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/daemonless/fjord/pkg/engine/appjail"
)

// Jails copy the host's resolv.conf (the LAN resolver), so siblings never
// find each other by name; appjail-dns + dnsmasq answer on every gateway.

// appjailAutoNetwork is what an empty virtualnet name means (AUTO_NETWORK_NAME).
const appjailAutoNetwork = "ajnet"

// appjailHosts is the hosts file appjail-dns keeps for dnsmasq.
const appjailHosts = "/var/tmp/appjail-hosts"

// jailDNSAnswers: dnsmasq resolves a name appjail-dns published. A new
// network is only listed once it has a jail, so any listed name will do.
var jailDNSAnswers = func(ctx context.Context) bool {
	b, err := os.ReadFile(appjailHosts)
	if err != nil {
		return false
	}
	var name string
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 1 {
			name = f[1]
			break
		}
	}
	if name == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, proto, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, proto, "127.0.0.1:53")
	}}
	addrs, err := r.LookupHost(ctx, name)
	return err == nil && len(addrs) > 0
}

// virtualnetGateway is a virtualnet's gateway address, "" when unknown.
var virtualnetGateway = func(ctx context.Context, network string) string {
	n, _ := appjail.Virtualnet(ctx, network)
	return n.Gateway
}

// setDirectorResolv points services sharing a virtualnet at its gateway's DNS
// (resolv.<network>.conf in dir); every other service loses the option.
func setDirectorResolv(ctx context.Context, directorYML, dir string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(directorYML), &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return directorYML, nil // reported by whatever parses it next
	}
	root := doc.Content[0]
	names := directorServiceNames(root)
	projNet, projOK := firstVirtualnet(mapKey(root, "options"))
	svcNet := map[string]string{}
	count := map[string]int{}
	for _, name := range names {
		svc := directorService(root, name)
		n, ok := firstVirtualnet(mapKey(svc, "options"))
		if !ok {
			n, ok = projNet, projOK
		}
		if ok && !hostNetworked(svc) {
			svcNet[name] = n
			count[n]++
		}
	}
	answers, asked := false, false
	for _, name := range names {
		svc := directorService(root, name)
		opts := mapKey(svc, "options")
		removeOption(opts, "resolv_conf")
		n := svcNet[name]
		if n == "" || count[n] < 2 {
			continue
		}
		if !asked {
			answers, asked = jailDNSAnswers(ctx), true
		}
		gw := ""
		if answers {
			gw = virtualnetGateway(ctx, n)
		}
		if gw == "" {
			continue
		}
		// Short jail names exist only on appjail's own network: search.
		file := filepath.Join(dir, "resolv."+n+".conf")
		if err := os.WriteFile(file, []byte("search "+n+".appjail\nnameserver "+gw+"\n"), 0o644); err != nil {
			return "", fmt.Errorf("jail DNS: %w", err)
		}
		if opts == nil {
			opts = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			setMapKey(svc, "options", opts)
		}
		opts.Content = append(opts.Content, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "resolv_conf"},
			{Kind: yaml.ScalarNode, Value: file},
		}})
	}
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	enc.Close()
	return sb.String(), nil
}

// firstVirtualnet is the network the first `virtualnet` option in opts joins.
func firstVirtualnet(opts *yaml.Node) (string, bool) {
	if opts == nil || opts.Kind != yaml.SequenceNode {
		return "", false
	}
	for _, item := range opts.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(item.Content); i += 2 {
			if item.Content[i].Value == "virtualnet" {
				n, _, _ := strings.Cut(item.Content[i+1].Value, ":")
				if n == "" {
					n = appjailAutoNetwork
				}
				return n, true
			}
		}
	}
	return "", false
}

func hasOption(opts *yaml.Node, key string) bool {
	if opts == nil {
		return false
	}
	for _, item := range opts.Content {
		if item.Kind == yaml.MappingNode && mapKey(item, key) != nil {
			return true
		}
	}
	return false
}

func removeOption(opts *yaml.Node, key string) {
	if opts == nil {
		return
	}
	kept := opts.Content[:0]
	for _, item := range opts.Content {
		if item.Kind == yaml.MappingNode && mapKey(item, key) != nil {
			continue
		}
		kept = append(kept, item)
	}
	opts.Content = kept
}

// hostNetworked: on the host's stack, siblings are 127.0.0.1.
func hostNetworked(svc *yaml.Node) bool {
	return hasOption(mapKey(svc, "options"), "ip4_inherit")
}
