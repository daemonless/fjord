package compose

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// DropVolumesReferencing removes each service volume mount whose source uses one
// of the given variables. It's used when an optional path variable resolves to
// empty: leaving "${MOVIES_PATH}:/movies" would render ":/movies" and break the
// compose, so the whole mount is dropped instead.
// DropTopLevelKey removes one top-level mapping key (e.g. "name") and
// returns the YAML; unparseable or key-less input comes back unchanged.
func DropTopLevelKey(composeYAML, key string) string {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return composeYAML
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			root.Content = append(root.Content[:i], root.Content[i+2:]...)
			out, err := encodeRoot(root)
			if err != nil {
				return composeYAML
			}
			return out
		}
	}
	return composeYAML
}

// DropPortsReferencing removes every service `ports:` entry that references
// one of vars -- an optional port the user left blank ("${PORT_443}:443"),
// which would otherwise make podman-compose choke on an empty host port.
func DropPortsReferencing(composeYAML string, vars []string) (string, error) {
	return dropSeqEntriesReferencing(composeYAML, "ports", vars)
}

// dropSeqEntriesReferencing removes scalar entries of the per-service sequence
// `key` (volumes, ports) that reference any of vars.
func dropSeqEntriesReferencing(composeYAML, key string, vars []string) (string, error) {
	if len(vars) == 0 {
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
		return composeYAML, nil
	}
	for i := 1; i < len(services.Content); i += 2 {
		svc := services.Content[i]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		seq := mapGet(svc, key)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		kept := seq.Content[:0:0]
		for _, item := range seq.Content {
			if item.Kind == yaml.ScalarNode && referencesAny(item.Value, vars) {
				continue
			}
			kept = append(kept, item)
		}
		seq.Content = kept
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

func DropVolumesReferencing(composeYAML string, vars []string) (string, error) {
	if len(vars) == 0 {
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
		return composeYAML, nil
	}
	for i := 1; i < len(services.Content); i += 2 {
		svc := services.Content[i]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		vols := mapGet(svc, "volumes")
		if vols == nil || vols.Kind != yaml.SequenceNode {
			continue
		}
		kept := vols.Content[:0:0]
		for _, item := range vols.Content {
			if item.Kind == yaml.ScalarNode && referencesAny(item.Value, vars) {
				continue
			}
			kept = append(kept, item)
		}
		vols.Content = kept
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

// varRef matches a compose variable reference: $VAR, ${VAR}, or ${VAR:-default}
// (and the other ${VAR<op>...} forms). Group 1 is the braced name, group 2 the
// bare one, group 3 the operator when the reference carries a default.
var varRef = regexp.MustCompile(`\$(?:\{([A-Za-z_][A-Za-z0-9_]*)(:?[-+?][^}]*)?\}|([A-Za-z_][A-Za-z0-9_]*))`)

// referencesAny reports whether s uses any of vars in a way that leaves the
// line unusable once that variable resolves to empty.
//
// Two things a substring test got wrong. Matching "$"+v swallowed every
// variable that merely STARTS with v, so an empty optional PORT dropped the
// "$PORT_HTTP" line next to it. And a reference that carries its own default
// -- "${PORT:-8080}:8080" -- still resolves to something usable, so dropping
// that line threw away a port mapping that would have published on 8080.
func referencesAny(s string, vars []string) bool {
	want := make(map[string]bool, len(vars))
	for _, v := range vars {
		want[v] = true
	}
	for _, m := range varRef.FindAllStringSubmatch(s, -1) {
		name, op := m[1], m[2]
		if name == "" {
			name = m[3]
		}
		if !want[name] {
			continue
		}
		// ${VAR:-x} and ${VAR:+x} test for empty, so an empty value still
		// yields x. ${VAR-x}/${VAR+x} test for UNSET, and fjord writes the
		// variable into .env as empty rather than leaving it out -- so those
		// resolve to empty too, and the line still has to go.
		if strings.HasPrefix(op, ":-") || strings.HasPrefix(op, ":+") {
			continue
		}
		return true
	}
	return false
}
