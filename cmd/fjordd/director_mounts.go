package main

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Storage edits for a director stack. The mounts API edited compose.yaml for
// every stack, and appjail-director never reads that file: a director stack's
// mounts are a project volume (name -> device) plus a service entry
// (- name: /path) in appjail-director.yml. The page then showed the edited
// compose's view, in which a director stack's variables resolve to nothing,
// so one Unmount looked like it took every mount with it while the jail kept
// them all. These edit the director file and hand back its view.

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

func dropMapKey(n *yaml.Node, key string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
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
	services := mapKey(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return false
	}
	for i := 1; i < len(services.Content); i += 2 {
		vols := mapKey(services.Content[i], "volumes")
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
	vols := mapKey(root, "volumes")
	name := base
	for n := 2; mapKey(vols, name) != nil; n++ {
		name = fmt.Sprintf("%s_%d", base, n)
	}
	return name
}

// directorMountName returns the project volume a service mounts at dest, or "".
func directorMountName(svc *yaml.Node, dest string) string {
	vols := mapKey(svc, "volumes")
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

// directorRemoveMount drops what service mounts at dest. A project volume no
// service mounts any more goes with it; director would otherwise keep a
// device nothing uses.
func directorRemoveMount(directorYML, service, dest string) (string, error) {
	root, err := directorDoc(directorYML)
	if err != nil {
		return "", err
	}
	svc, err := directorServiceOrErr(root, service)
	if err != nil {
		return "", err
	}
	var removed []string
	if vols := mapKey(svc, "volumes"); vols != nil && vols.Kind == yaml.SequenceNode {
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
	if project := mapKey(root, "volumes"); project != nil && project.Kind == yaml.MappingNode {
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

// directorAttachBindMount mounts hostPath at dest in service: a project
// volume with the host path as its device, and the service's entry for it.
func directorAttachBindMount(directorYML, service, hostPath, dest string, opts []string) (string, error) {
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
	project := mapKey(root, "volumes")
	if project == nil || project.Kind != yaml.MappingNode {
		project = &yaml.Node{Kind: yaml.MappingNode}
		setMapKey(root, "volumes", project)
	}
	setMapKey(project, name, body)
	vols := mapKey(svc, "volumes")
	if vols == nil || vols.Kind != yaml.SequenceNode {
		vols = &yaml.Node{Kind: yaml.SequenceNode}
		setMapKey(svc, "volumes", vols)
	}
	entry := &yaml.Node{Kind: yaml.MappingNode}
	setMapKey(entry, name, &yaml.Node{Kind: yaml.ScalarNode, Value: dest})
	vols.Content = append(vols.Content, entry)
	return encodeDirector(root)
}

// directorSetMountOptions sets or clears read-only on what service mounts at
// dest. The option sits on the project volume, so every service mounting it
// follows.
func directorSetMountOptions(directorYML, service, dest string, opts []string) (string, error) {
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
	body := mapKey(mapKey(root, "volumes"), name)
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
