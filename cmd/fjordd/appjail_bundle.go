package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/manifest"
	"gopkg.in/yaml.v3"
)

// varRefRe matches a ${VAR} reference, capturing the name.
var varRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// jailNameSanitize mirrors the appjail engine's jailName(): non-[A-Za-z0-9_]
// runs collapse to "_". Kept in sync so director jail names match what
// Status/Logs derive from the compose service names.
var jailNameSanitize = regexp.MustCompile(`[^a-zA-Z0-9_]`)

func directorJailName(stackID, service string) string {
	return jailNameSanitize.ReplaceAllString(stackID+"_"+service, "_")
}

// writeAppjailBundle materializes an AppJail director bundle into a stack dir:
// appjail-director.yml, Makejail, template.conf, and a filled .env. The appjail
// engine drives `appjail-director` for any stack whose dir has an
// appjail-director.yml (the file's presence is the per-stack "use director"
// switch; stacks without it stay on the legacy `appjail oci run` path). Returns
// the .env text written, so the caller records it on the stack.
func writeAppjailBundle(dir, stackID string, b *manifest.AppjailBundle, resolvedEnv map[string]string, composeYAML string, atts []composepkg.Attachment) (string, error) {
	if b.Director == "" {
		return "", fmt.Errorf("appjail bundle has no director file")
	}
	// container path -> resolved host path, from the compose fjord already
	// resolved. This is the join key between the director bundle and the
	// wizard: variable names diverge (compose CONFIG_DATA vs director
	// BAZARR_CONFIG_PATH), but both derive the container path from the same
	// x-daemonless volumes, so it is the one stable identifier.
	container2host := map[string]string{}
	for _, svc := range composepkg.ParseServices(composeYAML, resolvedEnv) {
		for _, vm := range svc.Volumes {
			if vm.Source != "" {
				container2host[vm.Dest] = vm.Source
			}
		}
	}

	directorYML, volPlaceholders, allVolVars, err := materializeDirector(b.Director, stackID, container2host)
	if err != nil {
		return "", fmt.Errorf("director.yml: %w", err)
	}
	// Place the project's jails on a host bridge instead of appjail's NAT
	// virtualnet. Done after materialize so it rewrites the finished document.
	if len(atts) > 0 {
		directorYML, err = setDirectorNetworks(directorYML, stackID, atts)
		if err != nil {
			return "", fmt.Errorf("network attach: %w", err)
		}
	}

	env := directorEnv(b.EnvDefaults, stackID, resolvedEnv, volPlaceholders, allVolVars)

	writes := []struct {
		name string
		body string
		mode os.FileMode
	}{
		{"appjail-director.yml", directorYML, 0o644},
		{"Makejail", b.Makejail, 0o644},
		{"template.conf", b.TemplateConf, 0o644},
		{".env", env, 0o600},
	}
	// Sidecar jail templates and other bundle extras go next to director.yml
	// under their own names (director references them as ${PWD}/<name>).
	// Names are file names only -- never a path that could escape the dir.
	for name, body := range b.Extras {
		if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
			return "", fmt.Errorf("appjail bundle: refusing extra file name %q", name)
		}
		writes = append(writes, struct {
			name string
			body string
			mode os.FileMode
		}{name, body, 0o644})
	}
	for _, wf := range writes {
		if wf.body == "" && wf.name != ".env" {
			continue // template.conf / Makejail may be absent
		}
		// Every file ends in exactly one newline: AppJail's Makejail parser
		// silently drops a final line with no trailing newline.
		body := strings.TrimRight(wf.body, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(dir, wf.name), []byte(body), wf.mode); err != nil {
			return "", err
		}
	}
	return env, nil
}

// materializeDirector fills the director.yml's volume devices from resolved
// host paths and prunes volumes fjord did not resolve (optional volumes the
// user left empty -- the same drop the compose path does, so director does not
// try to mount a bogus default). Returns the rewritten YAML and the set of
// volume placeholder -> host path that survived, for the .env.
func materializeDirector(directorYML, stackID string, container2host map[string]string) (string, map[string]string, map[string]bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(directorYML), &doc); err != nil {
		return "", nil, nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", nil, nil, fmt.Errorf("not a mapping")
	}
	root := doc.Content[0]

	// Namespace each service's jail name by the stack id: director names jails
	// globally (a bare `bazarr`), so two installs of one app would collide.
	// `<id>_<service>` also matches the engine's jailName(), so Status/Logs/Exec
	// find director jails with no director-specific code.
	if services := mapValue(root, "services"); services != nil {
		for i := 0; i+1 < len(services.Content); i += 2 {
			key, svc := services.Content[i].Value, services.Content[i+1]
			if svc.Kind != yaml.MappingNode {
				continue
			}
			jail := directorJailName(stackID, key)
			if n := mapValue(svc, "name"); n != nil {
				n.Value = jail
			} else {
				svc.Content = append(svc.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Value: "name"},
					&yaml.Node{Kind: yaml.ScalarNode, Value: jail})
			}
		}
	}

	// volume name -> container path, from services.<svc>.volumes: [{name: path}]
	volContainer := map[string]string{}
	if services := mapValue(root, "services"); services != nil {
		for i := 1; i < len(services.Content); i += 2 {
			svc := services.Content[i]
			vols := mapValue(svc, "volumes")
			if vols == nil || vols.Kind != yaml.SequenceNode {
				continue
			}
			for _, item := range vols.Content {
				if item.Kind == yaml.MappingNode && len(item.Content) >= 2 {
					volContainer[item.Content[0].Value] = item.Content[1].Value
				}
			}
		}
	}

	placeholders := map[string]string{} // surviving placeholder -> host path
	allVol := map[string]bool{}         // every device placeholder seen
	drop := map[string]bool{}           // volume names to prune
	// A wizard variable given several folders is rendered in the compose as
	// sub-mounts under its mount point (/movies/a, /movies/b) instead of one
	// ${VAR}:/movies bind. The director bundle only knows /movies, so expand
	// that one volume into one per sub-mount (MOVIES_PATH_1, MOVIES_PATH_2 ...)
	// rather than pruning it as "unresolved".
	type subMount struct{ name, cpath string }
	expand := map[string][]subMount{} // volume name -> replacement mounts

	volumes := mapValue(root, "volumes")
	if volumes != nil && volumes.Kind == yaml.MappingNode {
		var kept []*yaml.Node
		for i := 0; i+1 < len(volumes.Content); i += 2 {
			nameNode, body := volumes.Content[i], volumes.Content[i+1]
			volName := nameNode.Value
			placeholder := deviceVar(body)
			if placeholder != "" {
				allVol[placeholder] = true
			}
			cpath := volContainer[volName]
			host, resolved := container2host[cpath]
			if placeholder != "" && resolved {
				placeholders[placeholder] = host
				kept = append(kept, nameNode, body)
				continue
			}
			if placeholder != "" {
				var subs []string
				for dest := range container2host {
					if strings.HasPrefix(dest, strings.TrimRight(cpath, "/")+"/") {
						subs = append(subs, dest)
					}
				}
				sort.Strings(subs)
				for n, dest := range subs {
					sub := placeholder + "_" + strconv.Itoa(n+1)
					placeholders[sub] = container2host[dest]
					allVol[sub] = true
					dev := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!ENV", Style: yaml.SingleQuotedStyle, Value: "${" + sub + "}"}
					kept = append(kept,
						&yaml.Node{Kind: yaml.ScalarNode, Value: sub},
						&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "device"}, dev}})
					expand[volName] = append(expand[volName], subMount{sub, dest})
				}
				if len(subs) > 0 {
					continue
				}
			}
			// Unresolved (optional, left empty): drop this volume entirely.
			drop[volName] = true
		}
		volumes.Content = kept
	}

	// Rewrite each service's volume list: dropped volumes go, expanded ones are
	// replaced by their per-folder sub-mounts.
	if len(drop) > 0 || len(expand) > 0 {
		if services := mapValue(root, "services"); services != nil {
			for i := 1; i < len(services.Content); i += 2 {
				vols := mapValue(services.Content[i], "volumes")
				if vols == nil || vols.Kind != yaml.SequenceNode {
					continue
				}
				var kept []*yaml.Node
				for _, item := range vols.Content {
					if item.Kind == yaml.MappingNode && len(item.Content) >= 2 {
						key := item.Content[0].Value
						if drop[key] {
							continue
						}
						if subs, ok := expand[key]; ok {
							for _, sm := range subs {
								kept = append(kept, &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
									{Kind: yaml.ScalarNode, Value: sm.name}, {Kind: yaml.ScalarNode, Value: sm.cpath}}})
							}
							continue
						}
					}
					kept = append(kept, item)
				}
				vols.Content = kept
			}
		}
	}

	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", nil, nil, err
	}
	enc.Close()
	return buf.String(), placeholders, allVol, nil
}

// deviceVar returns the placeholder name in a volume body's `device: !ENV
// '${VAR}'`, or "" if the device is not a single ${VAR} reference.
func deviceVar(volBody *yaml.Node) string {
	if volBody == nil || volBody.Kind != yaml.MappingNode {
		return ""
	}
	dev := mapValue(volBody, "device")
	if dev == nil {
		return ""
	}
	if m := varRefRe.FindStringSubmatch(dev.Value); m != nil {
		return m[1]
	}
	return ""
}

// directorEnv builds the .env for a director-backed stack from the bundle's
// env_defaults schema, overriding: DIRECTOR_PROJECT (the stack id, so director
// keys its per-project state uniquely), scalar placeholders that name-match a
// resolved wizard value (WEB_PORT, PUID, PGID, TZ, DB_* -- dbuild's deploy mode
// names these to line up on purpose), and volume-device placeholders (from the
// container-path join). Placeholders for pruned volumes are omitted.
func directorEnv(envDefaults, stackID string, resolvedEnv, volPlaceholders map[string]string, allVolVars map[string]bool) string {
	env, order := parseEnvOrdered(envDefaults)

	setOrdered := func(k, v string) {
		if _, seen := env[k]; !seen {
			order = append(order, k)
		}
		env[k] = v
	}

	// Walk the schema: a volume-device placeholder (allVolVars) is filled from
	// the container-path join, or dropped if its volume was pruned; every other
	// key is a scalar, overridden by a name-matching resolved wizard value.
	kept := order[:0:0]
	for _, key := range order {
		if allVolVars[key] {
			if host, ok := volPlaceholders[key]; ok {
				env[key] = host
				kept = append(kept, key)
			} else {
				delete(env, key) // pruned volume
			}
			continue
		}
		if v, ok := resolvedEnv[key]; ok && v != "" {
			env[key] = v // scalar name-match override
		}
		kept = append(kept, key)
	}
	order = kept

	// Volume placeholders present in the join but not in env_defaults (belt and
	// braces) still need to be written.
	for k, v := range volPlaceholders {
		if _, seen := env[k]; !seen {
			setOrdered(k, v)
		}
	}

	setOrdered("DIRECTOR_PROJECT", stackID)

	// Values may reference other values (DATABASE_URL=postgresql://x:${DB_PASSWORD}@...).
	// podman-compose expands those while reading the compose; director's !ENV
	// tag substitutes once and never re-expands a value, so the jail would get
	// the literal "${DB_PASSWORD}". Expand here, against the final map.
	for _, k := range order {
		env[k] = composepkg.ExpandEnv(env[k], env)
	}

	var b strings.Builder
	b.WriteString("# Generated by fjord for appjail-director; edit values, not keys.\n")
	for _, k := range order {
		if env[k] == "" {
			continue
		}
		fmt.Fprintf(&b, "%s=%s\n", k, env[k])
	}
	return b.String()
}

// parseEnvOrdered parses .env text into a map plus the key order of first
// appearance (comments and blank lines skipped).
func parseEnvOrdered(text string) (map[string]string, []string) {
	env := map[string]string{}
	var order []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		if _, seen := env[k]; !seen {
			order = append(order, k)
		}
		env[k] = strings.TrimSpace(line[eq+1:])
	}
	return env, order
}

// mapValue returns the value node for key in a mapping node, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
