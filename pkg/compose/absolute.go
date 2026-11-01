package compose

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// AbsolutePaths rewrites the paths a compose file gives relative to its own
// folder -- bind-mount sources ("./config:/config", long-form source:) and
// env_file entries -- as absolute paths under dir. Importing a compose
// project moves its file into fjord's stack folder; left relative, "./config"
// would then name an empty folder there instead of the app's data.
func AbsolutePaths(composeYAML, dir string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return "", fmt.Errorf("parse compose: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose is not a YAML mapping")
	}
	root := doc.Content[0]
	abs := func(p string) string {
		if p == "." || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") {
			return filepath.Join(dir, p)
		}
		return p
	}
	services := mapGet(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return encodeRoot(root)
	}
	for i := 1; i < len(services.Content); i += 2 {
		svc := services.Content[i]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		if vols := mapGet(svc, "volumes"); vols != nil && vols.Kind == yaml.SequenceNode {
			for _, v := range vols.Content {
				switch v.Kind {
				case yaml.ScalarNode:
					src, rest, ok := strings.Cut(v.Value, ":")
					if ok && abs(src) != src {
						v.Value = abs(src) + ":" + rest
					}
				case yaml.MappingNode:
					if s := mapGet(v, "source"); s != nil && s.Kind == yaml.ScalarNode {
						s.Value = abs(s.Value)
					}
				}
			}
		}
		switch ef := mapGet(svc, "env_file"); {
		case ef == nil:
		case ef.Kind == yaml.ScalarNode:
			ef.Value = absEnvFile(ef.Value, dir)
		case ef.Kind == yaml.SequenceNode:
			for _, e := range ef.Content {
				if e.Kind == yaml.ScalarNode {
					e.Value = absEnvFile(e.Value, dir)
				} else if p := mapGet(e, "path"); p != nil && p.Kind == yaml.ScalarNode {
					p.Value = absEnvFile(p.Value, dir)
				}
			}
		}
	}
	return encodeRoot(root)
}

// absEnvFile: an env_file names a file relative to the compose's folder
// whether or not it starts with "./".
func absEnvFile(p, dir string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}
