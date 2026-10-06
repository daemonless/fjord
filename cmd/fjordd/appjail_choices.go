package main

import (
	"fmt"
	"maps"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/daemonless/fjord/pkg/manifest"
)

// AppJail runs the catalog's appjail-director.yml, which is the default
// answer to every choice: the compose an option rewrites is never read. So
// every non-default pick was refused on AppJail. The catalog now carries each
// option's AppJail form (the director services and volumes it adds, rendered
// by dbuild like the bundle itself); the bundle is changed the way the
// compose is: the picked options' jails added, the dropped services removed.

// appjailRefusal names a picked option AppJail cannot run: one that adds
// services and has no AppJail form (a catalog built before forms existed).
// "" when every pick can run.
func appjailRefusal(m *manifest.Manifest) string {
	for i, o := range m.Picked() {
		c := m.Choices[i]
		if o.Services != "" && o.Appjail == nil {
			return fmt.Sprintf("%s: %s has no AppJail form in this catalog; install it on podman, or refresh the catalog", c.Label, o.Label)
		}
	}
	return ""
}

// envRef is a ${VAR} a director references; PWD is the shell's own.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)`)

// withAppjailChoices returns the bundle with the picked options applied:
// their jails and volumes in the director, the services they drop gone along
// with volumes nothing mounts any more, their files beside director.yml, and
// every variable their jails read declared for the .env. b is not changed.
func withAppjailChoices(b *manifest.AppjailBundle, picked []*manifest.Option) (*manifest.AppjailBundle, error) {
	var drop []string
	var forms []*manifest.OptionAppjail
	for _, o := range picked {
		drop = append(drop, o.Drop...)
		if o.Appjail != nil {
			forms = append(forms, o.Appjail)
		}
	}
	if len(drop) == 0 && len(forms) == 0 {
		return b, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(b.Director), &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("director.yml: not a mapping")
	}
	root := doc.Content[0]
	services := ensureMap(root, "services")
	for _, name := range drop {
		deleteKey(services, name)
	}
	out := *b
	out.Extras = maps.Clone(b.Extras)
	if out.Extras == nil {
		out.Extras = map[string]string{}
	}
	for _, f := range forms {
		var frag yaml.Node
		if err := yaml.Unmarshal([]byte(f.Director), &frag); err != nil || len(frag.Content) == 0 {
			return nil, fmt.Errorf("an option's AppJail form: not YAML")
		}
		for _, key := range []string{"services", "volumes"} {
			add := mapValue(frag.Content[0], key)
			if add == nil || add.Kind != yaml.MappingNode {
				continue
			}
			into := ensureMap(root, key)
			for i := 0; i+1 < len(add.Content); i += 2 {
				deleteKey(into, add.Content[i].Value) // the form's own wins
				into.Content = append(into.Content, add.Content[i], add.Content[i+1])
			}
		}
		maps.Copy(out.Extras, f.Files)
	}
	pruneUnmountedVolumes(root)
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	enc.Close()
	out.Director = sb.String()
	// The added jails read their user, password and data folder from .env;
	// declared there, directorEnv writes their resolved values.
	declared, _ := parseEnvOrdered(b.EnvDefaults)
	var more []string
	for _, f := range forms {
		for _, m := range envRef.FindAllStringSubmatch(f.Director, -1) {
			if v := m[1]; v != "PWD" {
				if _, ok := declared[v]; !ok {
					declared[v] = ""
					more = append(more, v+"=")
				}
			}
		}
	}
	if len(more) > 0 {
		out.EnvDefaults = strings.TrimRight(b.EnvDefaults, "\n") + "\n" + strings.Join(more, "\n") + "\n"
	}
	return &out, nil
}

// appjailHostnames points each picked option's host variables at its jail
// (fjord names a director service's jail <stack>_<service>), so the app finds
// its database by the jail's name on the project's network. Not on host
// networking, where the jails share the host's addresses: 127.0.0.1 there.
func appjailHostnames(stackID string, picked []*manifest.Option) map[string]string {
	out := map[string]string{}
	for _, o := range picked {
		if o.Appjail == nil {
			continue
		}
		for svc, v := range o.Appjail.Hostnames {
			out[v] = directorJailName(stackID, svc)
		}
	}
	return out
}

func ensureMap(m *yaml.Node, key string) *yaml.Node {
	if v := mapValue(m, key); v != nil && v.Kind == yaml.MappingNode {
		return v
	}
	deleteKey(m, key)
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
	return v
}

func deleteKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// pruneUnmountedVolumes drops top-level volumes no service mounts: a dropped
// service's (immich's ML model cache without ML).
func pruneUnmountedVolumes(root *yaml.Node) {
	vols := mapValue(root, "volumes")
	if vols == nil || vols.Kind != yaml.MappingNode {
		return
	}
	used := map[string]bool{}
	if services := mapValue(root, "services"); services != nil {
		for i := 1; i < len(services.Content); i += 2 {
			list := mapValue(services.Content[i], "volumes")
			if list == nil {
				continue
			}
			for _, item := range list.Content {
				switch item.Kind {
				case yaml.MappingNode:
					for j := 0; j < len(item.Content); j += 2 {
						used[item.Content[j].Value] = true
					}
				case yaml.ScalarNode:
					name, _, _ := strings.Cut(item.Value, ":")
					used[strings.TrimSpace(name)] = true
				}
			}
		}
	}
	for i := 0; i+1 < len(vols.Content); {
		if !used[vols.Content[i].Value] {
			vols.Content = append(vols.Content[:i], vols.Content[i+2:]...)
			continue
		}
		i += 2
	}
}
