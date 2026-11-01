package main

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/stack"

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

// composeImageTag is the tag a stack's compose asks for the image the
// Makejail builds from. With one image, its tag; with more (a database a
// choice added), the one whose repository the Makejail's `from=` names.
func composeImageTag(composeYAML, makejail string) string {
	images, err := composepkg.ServiceImages(composeYAML)
	if err != nil || len(images) == 0 {
		return ""
	}
	if len(images) == 1 {
		return composepkg.ImageTag(images[0])
	}
	repo := makejailRepo(makejail)
	if repo == "" {
		return ""
	}
	tag := ""
	for _, img := range images {
		if t := composepkg.ImageTag(img); imageRepo(img) == repo && t != "" {
			if tag != "" && tag != t {
				return ""
			}
			tag = t
		}
	}
	return tag
}

// makejailRepo is the repository in a Makejail's `OPTION from=<repo>:...`.
func makejailRepo(makejail string) string {
	for _, line := range strings.Split(makejail, "\n") {
		if ref, ok := strings.CutPrefix(strings.TrimSpace(line), "OPTION from="); ok {
			return imageRepo(strings.TrimSpace(ref))
		}
	}
	return ""
}

// imageRepo is an image reference without its tag or digest.
func imageRepo(image string) string {
	image, _, _ = strings.Cut(image, "@")
	if t := composepkg.ImageTag(image); t != "" {
		image = strings.TrimSuffix(image, ":"+t)
	}
	return image
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
		// expose is a NAT redirect: on the host's stack there is no NAT to
		// redirect from, and the app already answers on the host's ports.
		if mode == composepkg.Host {
			dropExpose(node)
		}
		// Back to the template that carries ip4: a host-stack jail needs it,
		// and the .net variant is exactly the copy with it removed.
		retargetTemplates(node, false)
	}
	// The project's networking options apply to every jail on top of what
	// its service says, so a service put on the bridge here met the bundle's
	// own virtualnet a second time: "Virtual network `ajnet` cannot be used
	// again", exit 78, and no AppJail install on the default network came up.
	// The project keeps none of them; a service given no mode here and no
	// network of its own is handed the project's, so it stays where the
	// bundle put it.
	if projectNet := projectNetOptions(root); len(projectNet.Content) > 0 {
		for _, name := range names {
			if _, ok := modes[name]; ok {
				continue
			}
			node := directorService(root, name)
			if hasNetOptions(node) {
				continue
			}
			setServiceOptions(node, mergeServiceOptions(node, cloneSeq(projectNet)))
		}
		setDirectorOptions(root, mergeDirectorOptions(root, &yaml.Node{Kind: yaml.SequenceNode}))
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

// allHost puts every service of a director project on the host's stack, in
// the form setDirectorModes takes. The names are the director's own: it
// refuses one it does not have, and they need not match the compose's.
func allHost(directorYML string) (map[string]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(directorYML), &doc); err != nil {
		return nil, fmt.Errorf("parse director: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("director is not a YAML mapping")
	}
	modes := map[string]string{}
	for _, n := range directorServiceNames(doc.Content[0]) {
		modes[n] = composepkg.Host
	}
	return modes, nil
}

// servicesWithoutMakejail lists the director services that build from the
// project's own Makejail: those that name no makejail: of their own (director
// then uses "Makejail" next to the spec) or that name that file. A spec where
// every service names its own -- gh+AppJail-makejails/documentserver -- needs
// no local Makejail at all, and demanding one made people save a "#".
func servicesWithoutMakejail(director string) []string {
	var doc struct {
		Services map[string]struct {
			Makejail string `yaml:"makejail"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal([]byte(director), &doc) != nil {
		return nil
	}
	var out []string
	for name, svc := range doc.Services {
		switch strings.TrimPrefix(strings.TrimSpace(svc.Makejail), "./") {
		case "", "Makejail":
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// projectNetOptions is the project's networking options (directorNetOptions
// keys), in order, as a sequence of their own.
func projectNetOptions(root *yaml.Node) *yaml.Node {
	out := &yaml.Node{Kind: yaml.SequenceNode}
	if cur := mapKey(root, "options"); cur != nil && cur.Kind == yaml.SequenceNode {
		for _, item := range cur.Content {
			if item.Kind == yaml.MappingNode && len(item.Content) >= 1 && directorNetOptions[item.Content[0].Value] {
				out.Content = append(out.Content, item)
			}
		}
	}
	return out
}

// hasNetOptions says whether a service declares any networking option itself.
func hasNetOptions(svc *yaml.Node) bool {
	if cur := mapKey(svc, "options"); cur != nil && cur.Kind == yaml.SequenceNode {
		for _, item := range cur.Content {
			if item.Kind == yaml.MappingNode && len(item.Content) >= 1 && directorNetOptions[item.Content[0].Value] {
				return true
			}
		}
	}
	return false
}

// cloneSeq copies a sequence node so two services never share option nodes.
func cloneSeq(seq *yaml.Node) *yaml.Node {
	out := &yaml.Node{Kind: yaml.SequenceNode}
	for _, item := range seq.Content {
		c := *item
		c.Content = append([]*yaml.Node(nil), item.Content...)
		out.Content = append(out.Content, &c)
	}
	return out
}

// setDirectorImage makes one director service run image. A service naming its
// own `from:` gets that; one built from the Makejail gets the tag argument its
// `OPTION from=<repo>:${tag}` reads, digest and all: a tag of
// 'latest@sha256:...' builds that digest, and director rebuilds only that
// service (netlab, 2026-10-15). An image of another repository than the
// Makejail's cannot be said there, so it is refused rather than half-written.
func setDirectorImage(directorYML, service, image, makejailRepo string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(directorYML), &doc); err != nil {
		return "", fmt.Errorf("parse director: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("director is not a YAML mapping")
	}
	svc := directorService(doc.Content[0], service)
	if svc == nil {
		return "", fmt.Errorf("the director has no service %q", service)
	}
	if serviceHasOwnImage(svc) {
		forEachOption(svc, func(key string, val *yaml.Node) {
			if key == "from" {
				val.Value, val.Style, val.Tag = image, 0, "!!str"
			}
		})
	} else {
		if makejailRepo == "" || imageRepo(image) != makejailRepo {
			return "", fmt.Errorf("%s is built from %s; it cannot run %s", service, cmp.Or(makejailRepo, "its Makejail"), image)
		}
		setServiceArgument(svc, "tag", tagRef(image))
	}
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(doc.Content[0]); err != nil {
		return "", err
	}
	enc.Close()
	return sb.String(), nil
}

// tagRef is what follows an image's repository, as the Makejail's ${tag}
// takes it: "latest", "pkg@sha256:...". A digest with no tag gets latest in
// front, since the Makejail writes the colon.
func tagRef(image string) string {
	ref := strings.TrimPrefix(strings.TrimPrefix(image, imageRepo(image)), ":")
	if ref == "" || strings.HasPrefix(ref, "@") {
		ref = "latest" + ref
	}
	return ref
}

// directorFollowsCompose writes the compose's image for each named service
// (every one when none) into the director, so the version fjord shows and
// the one the jail runs are the same. A service the director does not have
// is left alone.
func directorFollowsCompose(st *stack.Stack, services []string) error {
	if strings.TrimSpace(st.Director) == "" {
		return nil
	}
	repo := makejailRepoOf(st)
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(st.Director), &doc); err != nil || len(doc.Content) == 0 {
		return fmt.Errorf("parse director: %v", err)
	}
	have := map[string]bool{}
	for _, n := range directorServiceNames(doc.Content[0]) {
		have[n] = true
	}
	list, err := composepkg.ServiceImageList(st.Compose)
	if err != nil {
		return err
	}
	env := st.EnvMap()
	sort.Slice(list, func(i, j int) bool { return list[i].Service < list[j].Service })
	for _, im := range list {
		if !have[im.Service] || (len(services) > 0 && !slices.Contains(services, im.Service)) {
			continue
		}
		if st.Director, err = setDirectorImage(st.Director, im.Service, composepkg.ExpandEnv(im.Image, env), repo); err != nil {
			return err
		}
	}
	return nil
}

// makejailRepoOf is the repository a director stack's Makejail builds from.
func makejailRepoOf(st *stack.Stack) string {
	mj := st.Makejail
	if mj == "" {
		b, _ := os.ReadFile(filepath.Join(st.Dir, "Makejail"))
		mj = string(b)
	}
	return makejailRepo(mj)
}

// directorImage is the image a director service runs: its own from:, or the
// Makejail's repository at its tag argument (the Makejail's ARG default when
// it has none). "" when the director has no such service.
func directorImage(st *stack.Stack, service string) string {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(st.Director), &doc); err != nil || len(doc.Content) == 0 {
		return ""
	}
	svc := directorService(doc.Content[0], service)
	if svc == nil {
		return ""
	}
	from := ""
	forEachOption(svc, func(key string, val *yaml.Node) {
		if key == "from" {
			from = val.Value
		}
	})
	if from != "" {
		return from
	}
	repo := makejailRepoOf(st)
	if repo == "" {
		return ""
	}
	tag := makejailTagDefault(st)
	if args := mapKey(svc, "arguments"); args != nil && args.Kind == yaml.SequenceNode {
		for _, item := range args.Content {
			if item.Kind == yaml.MappingNode && len(item.Content) >= 2 && item.Content[0].Value == "tag" {
				tag = item.Content[1].Value
			}
		}
	}
	return repo + ":" + tag
}

// makejailTagDefault is the Makejail's `ARG tag=<default>`, latest when it
// gives none.
func makejailTagDefault(st *stack.Stack) string {
	mj := st.Makejail
	if mj == "" {
		b, _ := os.ReadFile(filepath.Join(st.Dir, "Makejail"))
		mj = string(b)
	}
	for _, line := range strings.Split(mj, "\n") {
		if def, ok := strings.CutPrefix(strings.TrimSpace(line), "ARG tag="); ok && strings.TrimSpace(def) != "" {
			return strings.TrimSpace(def)
		}
	}
	return "latest"
}
