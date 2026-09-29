package compose

import (
	"encoding/json"
	"sort"

	"gopkg.in/yaml.v3"
)

// ChangedServices names the services whose definition differs between two
// versions of a stack, as podman-compose sees them: ${VAR}s filled in from each
// version's .env, comments and key order ignored. A service only in the new
// version counts; one only in the old does not -- up removes it anyway.
//
// A service with env_file also counts as changed when the .env does: the file
// lands in the container whether or not the compose names its variables.
//
// Apply uses this to recreate exactly what a Save changed. Leaving the choice
// to podman-compose is how zensical kept running its old config: the recreate
// was refused by an open exec session, compose restarted the old container and
// exited 0, and nothing said so.
func ChangedServices(oldCompose string, oldEnv map[string]string, newCompose string, newEnv map[string]string) []string {
	before := canonicalServices(oldCompose, oldEnv)
	after := canonicalServices(newCompose, newEnv)
	var out []string
	for name, def := range after {
		if before[name] != def {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// canonicalServices is each service's definition in a form that compares equal
// exactly when podman-compose would build the same container.
func canonicalServices(composeYAML string, env map[string]string) map[string]string {
	out := map[string]string{}
	var doc yaml.Node
	if yaml.Unmarshal([]byte(composeYAML), &doc) != nil || len(doc.Content) == 0 {
		return out
	}
	services := mapGet(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return out
	}
	envJSON, _ := json.Marshal(env) // encoding/json sorts map keys
	for i := 0; i+1 < len(services.Content); i += 2 {
		name, node := services.Content[i].Value, services.Content[i+1]
		text, err := yaml.Marshal(node)
		if err != nil {
			continue
		}
		var v any
		if yaml.Unmarshal([]byte(ExpandEnv(string(text), env)), &v) != nil {
			continue
		}
		b, err := json.Marshal(v)
		if err != nil {
			continue
		}
		def := string(b)
		if node.Kind == yaml.MappingNode && mapGet(node, "env_file") != nil {
			def += "\x00" + string(envJSON)
		}
		out[name] = def
	}
	return out
}
