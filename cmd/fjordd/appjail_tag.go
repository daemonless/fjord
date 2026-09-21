package main

import (
	"fmt"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"

	"gopkg.in/yaml.v3"
)

// setDirectorTag records the version a stack is meant to run where appjail can
// actually see it.
//
// appjail-director never opens compose.yaml. For a director stack the compose
// is fjord's own record, and a tag written only there changes nothing: the jail
// is built from the Makejail, whose `OPTION from=<image>:${tag}` takes its
// value from `ARG tag`, defaulting to latest. So installing zensical 0.0.59
// wrote 0.0.59 into the compose, ran latest, and left the update check
// comparing a version nothing was using against the registry -- an upgrade
// offered forever, and pressing it changed nothing because nothing was wrong.
//
// A service's `arguments:` are what feed ARG (director passes them as
// `appjail makejail ... -- --tag <value>`), so that is where it goes.
//
// Only services that take their image from the Makejail get it. One naming its
// own `from:` -- which is every service of a multi-image bundle -- does not use
// ${tag} at all, and writing an app's version next to a database image would be
// a lie even though nothing reads it.
func setDirectorTag(directorYML, tag string) (string, error) {
	if strings.TrimSpace(directorYML) == "" || tag == "" {
		return directorYML, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(directorYML), &doc); err != nil {
		return "", fmt.Errorf("parse director: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("director is not a YAML mapping")
	}
	root := doc.Content[0]
	changed := false
	for _, name := range directorServiceNames(root) {
		svc := directorService(root, name)
		if svc == nil || serviceHasOwnImage(svc) {
			continue
		}
		setServiceArgument(svc, "tag", tag)
		changed = true
	}
	if !changed {
		return directorYML, nil
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

// serviceHasOwnImage reports whether a service names its image directly rather
// than inheriting the Makejail's.
func serviceHasOwnImage(svc *yaml.Node) bool {
	found := false
	forEachOption(svc, func(key string, _ *yaml.Node) {
		if key == "from" {
			found = true
		}
	})
	return found
}

// setServiceArgument writes one `arguments:` entry, replacing any entry already
// under that name.
func setServiceArgument(svc *yaml.Node, name, value string) {
	entry := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: name},
		{Kind: yaml.ScalarNode, Value: value, Style: yaml.SingleQuotedStyle},
	}}
	args := mapKey(svc, "arguments")
	if args == nil || args.Kind != yaml.SequenceNode {
		out := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{entry}}
		setMapKey(svc, "arguments", out)
		return
	}
	for i, item := range args.Content {
		if item.Kind == yaml.MappingNode && len(item.Content) >= 1 && item.Content[0].Value == name {
			args.Content[i] = entry
			return
		}
	}
	args.Content = append(args.Content, entry)
}

// composeImageTag is the tag a stack's compose asks for, when every service
// agrees on one. A multi-image stack has no single answer, and its services
// name their own images anyway.
func composeImageTag(composeYAML string) string {
	images, err := composepkg.ServiceImages(composeYAML)
	if err != nil || len(images) != 1 {
		return ""
	}
	return composepkg.ImageTag(images[0])
}

// setDirectorModes puts named services on a built-in, in the director.
//
// A mode written into the compose does nothing for an appjail stack: appjail
// never opens compose.yaml, so a service set to "host" kept whatever the
// director said and the choice was silently ignored. The director expresses
// the same three things differently:
//
//	host    the jail shares this host's stack -- `alias` plus ip4/ip6_inherit,
//	        and the service's own template, which is where the ip4 that makes
//	        that work actually lives
//	bridge  appjail's own NAT network, which is what a bundle ships with
//	none    no networking options at all
func setDirectorModes(directorYML string, modes map[string]string) (string, error) {
	if len(modes) == 0 || strings.TrimSpace(directorYML) == "" {
		return directorYML, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(directorYML), &doc); err != nil {
		return "", fmt.Errorf("parse director: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("director is not a YAML mapping")
	}
	root := doc.Content[0]
	names := directorServiceNames(root)
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for svc, mode := range modes {
		if !have[svc] {
			return "", fmt.Errorf("this stack has no service named %q -- it has %s",
				svc, strings.Join(names, ", "))
		}
		node := directorService(root, svc)
		opts := &yaml.Node{Kind: yaml.SequenceNode}
		switch mode {
		case composepkg.Host:
			for _, k := range []string{"alias", "ip4_inherit", "ip6_inherit"} {
				opts.Content = append(opts.Content, &yaml.Node{
					Kind: yaml.MappingNode, Content: []*yaml.Node{
						{Kind: yaml.ScalarNode, Value: k},
						{Kind: yaml.ScalarNode, Tag: "!!null"},
					}})
			}
		case composepkg.Bridge:
			for _, kv := range [][2]string{{"virtualnet", ":<random> default"}, {"nat", ""}} {
				v := &yaml.Node{Kind: yaml.ScalarNode, Value: kv[1], Style: yaml.SingleQuotedStyle}
				if kv[1] == "" {
					v = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
				}
				opts.Content = append(opts.Content, &yaml.Node{
					Kind: yaml.MappingNode, Content: []*yaml.Node{
						{Kind: yaml.ScalarNode, Value: kv[0]}, v}})
			}
		}
		setServiceOptions(node, mergeServiceOptions(node, opts))
		// Back to the template that carries ip4: a host-stack jail needs it,
		// and the .net variant is exactly the copy with it removed.
		retargetTemplates(node, false)
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
