package compose

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// What a stack choice does to a compose (x-fjord.choices, applied at
// install): services added under services:, services dropped with every
// depends_on mention of them, a depends_on added so the app waits for a
// database the option brought.

// AddServices appends the services in servicesYAML (a fragment as written
// under `services:`, two-space indented) to the compose. A name already
// present is an error: an option never redefines what the compose has.
func AddServices(composeYAML, servicesYAML string) (string, error) {
	if strings.TrimSpace(servicesYAML) == "" {
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
	var frag yaml.Node
	if err := yaml.Unmarshal([]byte("services:\n"+indentFragment(servicesYAML)), &frag); err != nil {
		return "", fmt.Errorf("parse services to add: %w", err)
	}
	added := mapGet(frag.Content[0], "services")
	if added == nil || added.Kind != yaml.MappingNode {
		return "", fmt.Errorf("services to add are not a mapping")
	}
	services := mapGet(root, "services")
	if services == nil {
		root.Content = append(root.Content, scalar("services"), &yaml.Node{Kind: yaml.MappingNode})
		services = root.Content[len(root.Content)-1]
	}
	for i := 0; i+1 < len(added.Content); i += 2 {
		name := added.Content[i].Value
		if mapGet(services, name) != nil {
			return "", fmt.Errorf("service %q is already in the compose", name)
		}
		services.Content = append(services.Content, added.Content[i], added.Content[i+1])
	}
	return encodeRoot(root)
}

// DropServices removes the named services and every depends_on mention of
// them, so the compose stays one podman-compose accepts. Unknown names are
// ignored: dropping what is not there is not an error.
func DropServices(composeYAML string, names []string) (string, error) {
	if len(names) == 0 {
		return composeYAML, nil
	}
	gone := map[string]bool{}
	for _, n := range names {
		gone[n] = true
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
		return composeYAML, nil
	}
	kept := make([]*yaml.Node, 0, len(services.Content))
	for i := 0; i+1 < len(services.Content); i += 2 {
		if gone[services.Content[i].Value] {
			continue
		}
		kept = append(kept, services.Content[i], services.Content[i+1])
	}
	services.Content = kept
	for i := 1; i < len(services.Content); i += 2 {
		dep := mapGet(services.Content[i], "depends_on")
		if dep == nil {
			continue
		}
		switch dep.Kind {
		case yaml.SequenceNode:
			left := dep.Content[:0]
			for _, n := range dep.Content {
				if !gone[n.Value] {
					left = append(left, n)
				}
			}
			dep.Content = left
			if len(left) == 0 {
				removeKey(services.Content[i], "depends_on")
			}
		case yaml.MappingNode: // long form: name: {condition: ...}
			left := dep.Content[:0]
			for j := 0; j+1 < len(dep.Content); j += 2 {
				if !gone[dep.Content[j].Value] {
					left = append(left, dep.Content[j], dep.Content[j+1])
				}
			}
			dep.Content = left
		}
	}
	return encodeRoot(root)
}

// AddDependsOn makes service wait for deps (short form), keeping what it
// already waits for. A dep that is not a service is an error.
func AddDependsOn(composeYAML, service string, deps []string) (string, error) {
	if len(deps) == 0 {
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
	svc, err := serviceNode(root, service)
	if err != nil {
		return "", err
	}
	for _, d := range deps {
		if _, err := serviceNode(root, d); err != nil {
			return "", fmt.Errorf("depends_on %q: %w", d, err)
		}
	}
	dep := mapGet(svc, "depends_on")
	if dep == nil {
		svc.Content = append(svc.Content, scalar("depends_on"), &yaml.Node{Kind: yaml.SequenceNode})
		dep = svc.Content[len(svc.Content)-1]
	}
	if dep.Kind != yaml.SequenceNode {
		return "", fmt.Errorf("service %q uses the long depends_on form; cannot add to it", service)
	}
	dep.Style = 0 // block form, one name per line, whatever the list was before
	have := map[string]bool{}
	for _, n := range dep.Content {
		have[n.Value] = true
	}
	for _, d := range deps {
		if !have[d] {
			dep.Content = append(dep.Content, scalar(d))
		}
	}
	return encodeRoot(root)
}

// indentFragment puts a services fragment at the two-space indent `services:`
// expects, whatever indent it arrived with: a YAML block scalar may have
// kept the author's two spaces or dropped them.
func indentFragment(fragment string) string {
	lines := strings.Split(strings.TrimRight(fragment, "\n"), "\n")
	common := -1
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		n := len(ln) - len(strings.TrimLeft(ln, " "))
		if common < 0 || n < common {
			common = n
		}
	}
	if common < 0 {
		return ""
	}
	var b strings.Builder
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("  " + ln[common:] + "\n")
	}
	return b.String()
}

// removeKey drops key (and its value) from a mapping node.
func removeKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}
