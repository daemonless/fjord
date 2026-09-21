package main

import (
	"fmt"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"

	"gopkg.in/yaml.v3"
)

// serviceView is one service of a stack with everything it holds: what it runs,
// where it is on the network, and what is mounted into it.
//
// The UI had a stack's networks and volumes and no way to say which service
// they belonged to, because for a one-service stack there was nothing to say.
// A project with four jails on three networks cannot be described that way at
// all -- so the service, not the stack, is the thing this reports.
//
// Runtime facts (state, the address a jail actually holds) come from the
// engine; the rest is what the stack is CONFIGURED to do, which is the only
// thing that answers for a service that is stopped.
type serviceView struct {
	Name      string                  `json:"name"`
	Container string                  `json:"container,omitempty"`
	Image     string                  `json:"image,omitempty"`
	State     string                  `json:"state,omitempty"`
	Detail    string                  `json:"detail,omitempty"`
	Address   string                  `json:"address,omitempty"`
	HostNet   bool                    `json:"hostNetwork,omitempty"`
	Networks  []composepkg.Attachment `json:"networks,omitempty"`
	Volumes   []serviceVolume         `json:"volumes,omitempty"`
	Ports     []engine.Port           `json:"ports,omitempty"`
}

// serviceVolume is one mount, named the way the person who set it up thinks of
// it: a host path or a volume name, and where it lands inside.
type serviceVolume struct {
	Source   string `json:"source,omitempty"`
	Dest     string `json:"dest,omitempty"`
	Name     string `json:"name,omitempty"` // the volume's name, when it has one
	Kind     string `json:"kind,omitempty"` // bind | volume
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// stackServices describes every service of a stack. Director stacks are read
// from the director, compose stacks from the compose: for an appjail project
// the director IS the truth and appjail never looks at compose.yaml.
func stackServices(st *stack.Stack, status engine.StackStatus) []serviceView {
	var out []serviceView
	if strings.TrimSpace(st.Director) != "" {
		out = directorServiceViews(st)
	} else {
		out = composeServiceViews(st)
	}
	attachStatus(out, status)
	return out
}

// nameServiceIfaces fills in what THIS service's container calls each of its
// interfaces. podman numbers them from eth0 in attachment order, per container.
func nameServiceIfaces(atts []composepkg.Attachment) []composepkg.Attachment {
	out := make([]composepkg.Attachment, len(atts))
	for i, a := range atts {
		a.Iface = fmt.Sprintf("eth%d", i)
		out[i] = a
	}
	return out
}

func composeServiceViews(st *stack.Stack) []serviceView {
	env := st.EnvMap()
	byService := composepkg.ServiceAttachments(st.Compose)
	var out []serviceView
	for _, svc := range composepkg.ParseServices(st.Compose, env) {
		v := serviceView{
			Name:  svc.Name,
			Image: svc.Image,
			// Named PER SERVICE: the numbering restarts inside every
			// container, so the stack-level pass over one flat list gave the
			// second service eth1 for its only interface. It never reached
			// this view either, so the editor's Interface column said "on
			// create" for every row of a stack that had been running for days.
			HostNet:  svc.NetworkHost,
			Networks: nameServiceIfaces(byService[svc.Name]),
		}
		for _, p := range svc.Ports {
			v.Ports = append(v.Ports, engine.Port{
				HostPort: p.Host, ContainerPort: p.Container, Protocol: p.Proto})
		}
		for _, m := range svc.Volumes {
			v.Volumes = append(v.Volumes, serviceVolume{
				Source: m.Source, Name: m.Name, Dest: m.Dest, ReadOnly: m.ReadOnly,
				Kind: mountKind(m.Source + m.Name)})
		}
		out = append(out, v)
	}
	return out
}

// directorServiceViews reads a director project: the image from each service's
// `from:` option, the mounts from its `volumes:` resolved through the project's
// volume devices, and the networks from the options -- per service, since that
// is where fjord writes them.
func directorServiceViews(st *stack.Stack) []serviceView {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(st.Director), &doc) != nil || len(doc.Content) == 0 ||
		doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	root := doc.Content[0]
	env := st.EnvMap()
	project, perSvc, order := directorAttachmentsByService(st.Director)
	devices := directorVolumeDevices(root, env)

	var out []serviceView
	for _, name := range order {
		svc := directorService(root, name)
		v := serviceView{Name: name, Container: directorServiceJail(svc, st.Name, name)}
		forEachOption(svc, func(key string, val *yaml.Node) {
			switch key {
			case "from":
				v.Image = composepkg.ExpandEnv(val.Value, env)
			case "alias", "ip4_inherit", "ip6_inherit":
				v.HostNet = true
			}
		})
		// A service with none of its own is on whatever the project declares:
		// that is where attachments lived before fjord wrote them per service,
		// and a bundle can still put them there.
		if v.Networks = perSvc[name]; len(v.Networks) == 0 {
			for _, att := range project {
				att.Service = name
				v.Networks = append(v.Networks, att)
			}
		}
		v.Volumes = directorServiceVolumes(svc, devices)
		out = append(out, v)
	}
	return out
}

// directorServiceJail is the jail name a service runs as: its `name:` when the
// bundle gives one, otherwise the name fjord derives.
func directorServiceJail(svc *yaml.Node, stackID, service string) string {
	if n := mapKey(svc, "name"); n != nil && n.Value != "" {
		return n.Value
	}
	return directorJailName(stackID, service)
}

// directorVolumeDevices maps a project volume's name to the host path behind
// it. The device is usually an !ENV placeholder, so it is resolved through the
// stack's own .env -- an unresolved ${UPLOAD_LOCATION} tells nobody anything.
func directorVolumeDevices(root *yaml.Node, env map[string]string) map[string]string {
	out := map[string]string{}
	vols := mapKey(root, "volumes")
	if vols == nil || vols.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(vols.Content); i += 2 {
		name, body := vols.Content[i].Value, vols.Content[i+1]
		if body.Kind != yaml.MappingNode {
			continue
		}
		if dev := mapKey(body, "device"); dev != nil {
			out[name] = composepkg.ExpandEnv(dev.Value, env)
		}
	}
	return out
}

// directorServiceVolumes turns a service's `volumes:` list -- entries of
// `- <volume name>: <container path>` -- into mounts.
func directorServiceVolumes(svc *yaml.Node, devices map[string]string) []serviceVolume {
	vols := mapKey(svc, "volumes")
	if vols == nil || vols.Kind != yaml.SequenceNode {
		return nil
	}
	var out []serviceVolume
	for _, item := range vols.Content {
		if item.Kind != yaml.MappingNode || len(item.Content) < 2 {
			continue
		}
		name, dest := item.Content[0].Value, item.Content[1].Value
		out = append(out, serviceVolume{
			Name: name, Source: devices[name], Dest: dest,
			Kind: mountKind(devices[name])})
	}
	return out
}

// forEachOption visits each `- key: value` in a service's options list.
func forEachOption(svc *yaml.Node, fn func(key string, val *yaml.Node)) {
	opts := mapKey(svc, "options")
	if opts == nil || opts.Kind != yaml.SequenceNode {
		return
	}
	for _, item := range opts.Content {
		if item.Kind == yaml.MappingNode && len(item.Content) >= 2 {
			fn(item.Content[0].Value, item.Content[1])
		}
	}
}

// reachableAddress is the address a service holds on a segment something off
// this host can get to, or "" when it holds none.
func reachableAddress(atts []composepkg.Attachment) string {
	for _, a := range atts {
		if a.IP != "" && ownAddress(a.Network) {
			return a.IP
		}
	}
	return ""
}

func mountKind(source string) string {
	if strings.HasPrefix(source, "/") {
		return "bind"
	}
	if source == "" {
		return ""
	}
	return "volume"
}

// attachStatus puts the runtime half onto each service: what is actually
// running and what address it actually holds.
//
// The engine reports containers, which are not named after services -- a jail
// is immich_database for the service `database`. Matched by the name the
// config gives, then by the conventional derivations, and only then by
// position, which is right exactly when nothing has been renamed.
func attachStatus(views []serviceView, status engine.StackStatus) {
	if len(views) == 0 || len(status.Containers) == 0 {
		return
	}
	byName := make(map[string]*engine.ContainerStatus, len(status.Containers))
	taken := make(map[string]bool, len(status.Containers))
	for i := range status.Containers {
		byName[status.Containers[i].Name] = &status.Containers[i]
	}
	find := func(v *serviceView) *engine.ContainerStatus {
		for _, cand := range []string{v.Container, v.Name} {
			if cand == "" {
				continue
			}
			if c, ok := byName[cand]; ok && !taken[c.Name] {
				return c
			}
		}
		// podman-compose names a container <project>_<service>_<n>.
		for name, c := range byName {
			if taken[name] {
				continue
			}
			if mid := "_" + v.Name + "_"; strings.Contains(name, mid) ||
				strings.HasSuffix(name, "_"+v.Name) {
				return c
			}
		}
		return nil
	}
	for i := range views {
		c := find(&views[i])
		if c == nil {
			continue
		}
		taken[c.Name] = true
		views[i].Container, views[i].State = c.Name, c.State
		views[i].Detail, views[i].Address = c.Detail, c.Address
		if len(c.Ports) > 0 {
			views[i].Ports = c.Ports
		}
		// The address it actually holds on each network, which the config
		// only knows when someone pinned one.
		for j := range views[i].Networks {
			if addr := c.Addresses[views[i].Networks[j].Network]; addr != "" {
				views[i].Networks[j].IP = addr
			}
		}
		// One address for the service, and it should be the one that is any
		// use. The engine reports whichever it resolved first, which for
		// immich-server was its private 10.100.x -- so the row said the app
		// lived somewhere no browser can reach while the Open button, which
		// asks a different question, said 192.168.4.114.
		if a := reachableAddress(views[i].Networks); a != "" {
			views[i].Address = a
		}
	}
	// Nothing matched by name and the counts agree: the engine lists them in
	// the order the config declares them, so position is the answer.
	if len(taken) == 0 && len(views) == len(status.Containers) {
		for i := range views {
			c := status.Containers[i]
			views[i].Container, views[i].State = c.Name, c.State
			views[i].Detail, views[i].Address = c.Detail, c.Address
			if len(c.Ports) > 0 {
				views[i].Ports = c.Ports
			}
		}
	}
}

// linkHost is the address a browser should use to reach this stack, or "".
//
// Only the daemon can answer it: it needs each service's address ON EACH
// network plus whether that network puts a container somewhere reachable.
// The UI used to guess from the stack's first network, which for a stack
// whose app is on the LAN and whose database is on a private segment picked
// the private one and offered a link nothing could open.
func linkHost(views []serviceView, reachable func(string) bool) string {
	for _, v := range views {
		for _, n := range v.Networks {
			if n.IP != "" && reachable(n.Network) {
				return n.IP
			}
		}
	}
	return ""
}
