package main

import (
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/stack"

	"gopkg.in/yaml.v3"
)

// Daemonless images print a startup banner, mono unless the container's
// environment asks for color: a pager reading the log takes the color logo
// for a binary file and stops on a prompt. fjord reads logs without one and
// its log pane renders the colors, so the stacks it installs ask. A value
// the stack sets itself, in its compose, its bundle or its .env, stands;
// other images get nothing, the variable means nothing to them.
const (
	bannerVar     = "DAEMONLESS_BANNER"
	bannerDefault = "color"
)

// bannerCompose hands ${DAEMONLESS_BANNER} to each daemonless service of a
// compose that does not set it already.
func bannerCompose(composeYAML string, env map[string]string) (string, error) {
	for _, svc := range composepkg.ParseServices(composeYAML, env) {
		if !composepkg.DaemonlessImage(svc.Image) {
			continue
		}
		out, err := composepkg.PassEnv(composeYAML, svc.Name, []string{bannerVar})
		if err != nil {
			return "", err
		}
		composeYAML = out
	}
	return composeYAML, nil
}

// setDirectorBanner is bannerCompose for a director bundle: each jail built
// from a daemonless image reads ${DAEMONLESS_BANNER} in its oci environment,
// unless the bundle wrote an entry under that name.
func setDirectorBanner(directorYML string, b *manifest.AppjailBundle) (string, error) {
	if strings.TrimSpace(directorYML) == "" {
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
		if svc == nil || !composepkg.DaemonlessImage(directorServiceImage(svc, b)) {
			continue
		}
		if addOCIEnv(svc, bannerVar) {
			changed = true
		}
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

// directorServiceImage is the image a service's jail is built from: its own
// from option, else the from= of the Makejail it names (a bundle extra), else
// the bundle's.
func directorServiceImage(svc *yaml.Node, b *manifest.AppjailBundle) string {
	from, makejail := "", ""
	forEachOption(svc, func(key string, val *yaml.Node) {
		switch key {
		case "from":
			from = val.Value
		case "makejail":
			makejail = val.Value
		}
	})
	if from != "" {
		return from
	}
	if makejail != "" {
		if body, ok := b.Extras[path.Base(makejail)]; ok {
			return makejailRepo(body)
		}
	}
	return makejailRepo(b.Makejail)
}

// addOCIEnv adds `- NAME: !ENV '${NAME}'` to a service's oci environment,
// unless an entry under that name is there. Reports whether it did.
func addOCIEnv(svc *yaml.Node, name string) bool {
	oci := mapKey(svc, "oci")
	if oci == nil || (oci.Kind == yaml.ScalarNode && oci.Tag == "!!null") {
		oci = &yaml.Node{Kind: yaml.MappingNode}
		setMapKey(svc, "oci", oci)
	}
	if oci.Kind != yaml.MappingNode {
		return false
	}
	env := mapKey(oci, "environment")
	if env == nil || (env.Kind == yaml.ScalarNode && env.Tag == "!!null") {
		env = &yaml.Node{Kind: yaml.SequenceNode}
		setMapKey(oci, "environment", env)
	}
	ref := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!ENV", Style: yaml.SingleQuotedStyle, Value: "${" + name + "}"}
	switch env.Kind {
	case yaml.SequenceNode:
		for _, it := range env.Content {
			switch {
			case it.Kind == yaml.MappingNode && len(it.Content) >= 1 && it.Content[0].Value == name:
				return false
			case it.Kind == yaml.ScalarNode:
				if k, _, _ := strings.Cut(it.Value, "="); k == name {
					return false
				}
			}
		}
		env.Content = append(env.Content, &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: name}, ref}})
	case yaml.MappingNode:
		if mapKey(env, name) != nil {
			return false
		}
		setMapKey(env, name, ref)
	default:
		return false
	}
	return true
}

// appendBannerEnv appends DAEMONLESS_BANNER=color to an .env body when it is
// not already defined there.
func appendBannerEnv(env string, parsed map[string]string) string {
	if _, ok := parsed[bannerVar]; ok {
		return env
	}
	if env != "" && !strings.HasSuffix(env, "\n") {
		env += "\n"
	}
	return env + bannerVar + "=" + bannerDefault + "\n"
}

// bannerAdoptSpec adds ${DAEMONLESS_BANNER} and DAEMONLESS_BANNER=color to an
// adopted stack's compose or director spec when it runs a daemonless image and
// does not set DAEMONLESS_BANNER itself.
func bannerAdoptSpec(a *engine.AdoptSpec) *engine.AdoptSpec {
	if a == nil {
		return nil
	}
	envMap := (&stack.Stack{Env: a.Env}).EnvMap()
	if a.Compose != "" {
		if c, err := bannerCompose(a.Compose, envMap); err == nil {
			a.Compose = c
		}
	}
	if a.Director != "" {
		b := &manifest.AppjailBundle{Director: a.Director, Makejail: a.Makejail}
		if d, err := setDirectorBanner(a.Director, b); err == nil {
			a.Director = d
		}
	}
	if strings.Contains(a.Compose, "${"+bannerVar+"}") || strings.Contains(a.Director, "${"+bannerVar+"}") {
		a.Env = appendBannerEnv(a.Env, envMap)
	}
	return a
}

// ensureStackBanner updates a stack's compose/director and .env in memory with
// DAEMONLESS_BANNER=color when it runs a daemonless image and does not yet set
// DAEMONLESS_BANNER. Reports whether anything changed.
func ensureStackBanner(st *stack.Stack) bool {
	if st == nil {
		return false
	}
	origCompose, origDirector, origEnv := st.Compose, st.Director, st.Env
	envMap := st.EnvMap()
	if st.Compose != "" {
		if c, err := bannerCompose(st.Compose, envMap); err == nil {
			st.Compose = c
		}
	}
	if st.Director != "" {
		extras := map[string]string{}
		if st.Dir != "" {
			if matches, err := filepath.Glob(filepath.Join(st.Dir, "*.Makejail")); err == nil {
				for _, m := range matches {
					if b, err := os.ReadFile(m); err == nil {
						extras[filepath.Base(m)] = string(b)
					}
				}
			}
		}
		b := &manifest.AppjailBundle{Director: st.Director, Makejail: st.Makejail, Extras: extras}
		if d, err := setDirectorBanner(st.Director, b); err == nil {
			st.Director = d
		}
	}
	if strings.Contains(st.Compose, "${"+bannerVar+"}") || strings.Contains(st.Director, "${"+bannerVar+"}") {
		st.Env = appendBannerEnv(st.Env, envMap)
	}
	return st.Compose != origCompose || st.Director != origDirector || st.Env != origEnv
}

// backfillStackBanners adds DAEMONLESS_BANNER=color to stacks adopted or
// installed before bannerCompose existed. Best-effort, once per start.
func backfillStackBanners(m *stack.Manager) {
	list, err := m.List()
	if err != nil {
		return
	}
	n := 0
	for _, item := range list {
		st, err := m.Get(item.Name)
		if err != nil {
			continue
		}
		if ensureStackBanner(st) {
			if err := m.Save(st); err == nil {
				n++
			}
		}
	}
	if n > 0 {
		log.Printf("stack banners: added %s=%s to %d stack(s)", bannerVar, bannerDefault, n)
	}
}
