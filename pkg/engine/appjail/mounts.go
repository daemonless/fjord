package appjail

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Engine aliases Backend so callers and tests referring to either type name
// share the same SpecMountEditor implementation.
type Engine = Backend

// AttachSpecBindMount implements engine.SpecMountEditor.
func (e *Engine) AttachSpecBindMount(specYML, service, hostPath, dest string, opts []string) (string, error) {
	return AttachBindMount(specYML, service, hostPath, dest, opts)
}

// RemoveSpecMount implements engine.SpecMountEditor.
func (e *Engine) RemoveSpecMount(specYML, service, dest string) (string, error) {
	return RemoveMount(specYML, service, dest)
}

// SetSpecMountOptions implements engine.SpecMountEditor.
func (e *Engine) SetSpecMountOptions(specYML, service, dest string, opts []string) (string, error) {
	return SetMountOptions(specYML, service, dest, opts)
}

// directorDoc parses a director file to its root mapping.
func directorDoc(directorYML string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(directorYML), &doc); err != nil {
		return nil, fmt.Errorf("parse director file: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("director file is not a YAML mapping")
	}
	return doc.Content[0], nil
}

func encodeDirector(root *yaml.Node) (string, error) {
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	enc.Close()
	return sb.String(), nil
}

func setMapKey(n *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = val
			return
		}
	}
	n.Content = append(n.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key}, val)
}

func dropMapKey(n *yaml.Node, key string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
}

func directorService(root *yaml.Node, name string) *yaml.Node {
	services := yamlMapValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(services.Content); i += 2 {
		if services.Content[i].Value == name {
			return services.Content[i+1]
		}
	}
	return nil
}

// directorMountOptions checks what a director mount can carry: fstab options,
// of which the page offers read-only. The SELinux labels and U are podman's.
func directorMountOptions(opts []string) (ro bool, err error) {
	for _, o := range opts {
		switch o {
		case "ro":
			ro = true
		case "rw", "":
		default:
			return false, fmt.Errorf("%q is a podman mount option; an AppJail mount takes read-only only", o)
		}
	}
	return ro, nil
}

// directorVolumeReferenced reports whether any service still mounts the
// project volume.
func directorVolumeReferenced(root *yaml.Node, name string) bool {
	services := yamlMapValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return false
	}
	for i := 1; i < len(services.Content); i += 2 {
		vols := yamlMapValue(services.Content[i], "volumes")
		if vols == nil || vols.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range vols.Content {
			if item.Kind == yaml.MappingNode && len(item.Content) >= 2 && item.Content[0].Value == name {
				return true
			}
		}
	}
	return false
}

// directorVolumeName makes a project volume name for a new mount from its
// container path (/media/books -> MEDIA_BOOKS), unique among the project's.
func directorVolumeName(root *yaml.Node, dest string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(dest) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
			b.WriteRune('_')
		}
	}
	base := strings.Trim(b.String(), "_")
	if base == "" {
		base = "MOUNT"
	}
	vols := yamlMapValue(root, "volumes")
	name := base
	for n := 2; yamlMapValue(vols, name) != nil; n++ {
		name = fmt.Sprintf("%s_%d", base, n)
	}
	return name
}

// directorMountName returns the project volume a service mounts at dest, or "".
func directorMountName(svc *yaml.Node, dest string) string {
	vols := yamlMapValue(svc, "volumes")
	if vols == nil || vols.Kind != yaml.SequenceNode {
		return ""
	}
	for _, item := range vols.Content {
		if item.Kind == yaml.MappingNode && len(item.Content) >= 2 && item.Content[1].Value == dest {
			return item.Content[0].Value
		}
	}
	return ""
}

func directorServiceOrErr(root *yaml.Node, service string) (*yaml.Node, error) {
	svc := directorService(root, service)
	if svc == nil {
		return nil, fmt.Errorf("no service %q in the director file", service)
	}
	return svc, nil
}

// RemoveMount drops what service mounts at dest. A project volume no
// service mounts any more goes with it; director would otherwise keep a
// device nothing uses.
func RemoveMount(directorYML, service, dest string) (string, error) {
	root, err := directorDoc(directorYML)
	if err != nil {
		return "", err
	}
	svc, err := directorServiceOrErr(root, service)
	if err != nil {
		return "", err
	}
	var removed []string
	if vols := yamlMapValue(svc, "volumes"); vols != nil && vols.Kind == yaml.SequenceNode {
		kept := vols.Content[:0]
		for _, item := range vols.Content {
			if item.Kind == yaml.MappingNode && len(item.Content) >= 2 && item.Content[1].Value == dest {
				removed = append(removed, item.Content[0].Value)
				continue
			}
			kept = append(kept, item)
		}
		vols.Content = kept
		if len(kept) == 0 {
			dropMapKey(svc, "volumes")
		}
	}
	if len(removed) == 0 {
		return "", fmt.Errorf("%s has nothing mounted at %s", service, dest)
	}
	if project := yamlMapValue(root, "volumes"); project != nil && project.Kind == yaml.MappingNode {
		for _, name := range removed {
			if !directorVolumeReferenced(root, name) {
				dropMapKey(project, name)
			}
		}
		if len(project.Content) == 0 {
			dropMapKey(root, "volumes")
		}
	}
	return encodeDirector(root)
}

// AttachBindMount mounts hostPath at dest in service: a project
// volume with the host path as its device, and the service's entry for it.
func AttachBindMount(directorYML, service, hostPath, dest string, opts []string) (string, error) {
	if !strings.HasPrefix(hostPath, "/") || !strings.HasPrefix(dest, "/") {
		return "", fmt.Errorf("both host and container paths must be absolute")
	}
	ro, err := directorMountOptions(opts)
	if err != nil {
		return "", err
	}
	root, err := directorDoc(directorYML)
	if err != nil {
		return "", err
	}
	svc, err := directorServiceOrErr(root, service)
	if err != nil {
		return "", err
	}
	if directorMountName(svc, dest) != "" {
		return "", fmt.Errorf("%q is already mounted", dest)
	}
	name := directorVolumeName(root, dest)
	body := &yaml.Node{Kind: yaml.MappingNode}
	setMapKey(body, "device", &yaml.Node{Kind: yaml.ScalarNode, Value: hostPath})
	if ro {
		setMapKey(body, "options", &yaml.Node{Kind: yaml.ScalarNode, Value: "ro"})
	}
	project := yamlMapValue(root, "volumes")
	if project == nil || project.Kind != yaml.MappingNode {
		project = &yaml.Node{Kind: yaml.MappingNode}
		setMapKey(root, "volumes", project)
	}
	setMapKey(project, name, body)
	vols := yamlMapValue(svc, "volumes")
	if vols == nil || vols.Kind != yaml.SequenceNode {
		vols = &yaml.Node{Kind: yaml.SequenceNode}
		setMapKey(svc, "volumes", vols)
	}
	entry := &yaml.Node{Kind: yaml.MappingNode}
	setMapKey(entry, name, &yaml.Node{Kind: yaml.ScalarNode, Value: dest})
	vols.Content = append(vols.Content, entry)
	return encodeDirector(root)
}

// SetMountOptions sets or clears read-only on what service mounts at
// dest. The option sits on the project volume, so every service mounting it
// follows.
func SetMountOptions(directorYML, service, dest string, opts []string) (string, error) {
	ro, err := directorMountOptions(opts)
	if err != nil {
		return "", err
	}
	root, err := directorDoc(directorYML)
	if err != nil {
		return "", err
	}
	svc, err := directorServiceOrErr(root, service)
	if err != nil {
		return "", err
	}
	name := directorMountName(svc, dest)
	if name == "" {
		return "", fmt.Errorf("%s has nothing mounted at %s", service, dest)
	}
	body := yamlMapValue(yamlMapValue(root, "volumes"), name)
	if body == nil || body.Kind != yaml.MappingNode {
		return "", fmt.Errorf("the director file declares no volume %q", name)
	}
	if ro {
		setMapKey(body, "options", &yaml.Node{Kind: yaml.ScalarNode, Value: "ro"})
	} else {
		dropMapKey(body, "options")
	}
	return encodeDirector(root)
}
