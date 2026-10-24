package compose

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// PassEnv hands .env values to a service: "NAME=${NAME}" in its environment
// for each of names it does not set already. A value in .env that nothing
// names reaches no container (openssh-server's USER_NAME typed into .env
// alone), and the .env tab says so; this is the fix it offers. Pass
// FirstService for a stack with one.
func PassEnv(composeYAML, service string, names []string) (string, error) {
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
	env := mapGet(svc, "environment")
	if env == nil {
		svc.Content = append(svc.Content, scalar("environment"), &yaml.Node{Kind: yaml.SequenceNode})
		env = svc.Content[len(svc.Content)-1]
	}
	set := map[string]bool{}
	switch env.Kind {
	case yaml.SequenceNode:
		for _, it := range env.Content {
			name, _, _ := strings.Cut(it.Value, "=")
			set[name] = true
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(env.Content); i += 2 {
			set[env.Content[i].Value] = true
		}
	default:
		return "", fmt.Errorf("the service's environment is neither a list nor a mapping")
	}
	for _, n := range names {
		if n == "" || set[n] {
			continue
		}
		set[n] = true
		if env.Kind == yaml.SequenceNode {
			env.Content = append(env.Content, scalar(n+"=${"+n+"}"))
		} else {
			env.Content = append(env.Content, scalar(n), scalar("${"+n+"}"))
		}
	}
	return encodeRoot(root)
}
