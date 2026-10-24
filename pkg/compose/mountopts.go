package compose

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// MountOptions splits what follows a short-form mount's destination
// ("ro,Z") into its options. "" has none.
func MountOptions(s string) []string {
	var out []string
	for _, o := range strings.Split(s, ",") {
		if o = strings.TrimSpace(o); o != "" && !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	return out
}

// podmanMountOptions is every option podman takes after a volume's
// destination (podman-run(1), --volume). Anything else fails at start with
// "invalid option type", long after the Save that wrote it.
var podmanMountOptions = map[string]bool{
	"ro": true, "rw": true, "z": true, "Z": true, "U": true, "O": true,
	"copy": true, "nocopy": true, "exec": true, "noexec": true,
	"suid": true, "nosuid": true, "dev": true, "nodev": true,
	"bind": true, "rbind": true, "idmap": true,
	"shared": true, "rshared": true, "slave": true, "rslave": true,
	"private": true, "rprivate": true, "unbindable": true, "runbindable": true,
}

// CheckMountOptions refuses what podman on goos cannot mount: an option it
// does not know, ro with rw, or the overlay option O on FreeBSD ("read/write
// overlay mounts not supported on freebsd"). nil when all are usable.
func CheckMountOptions(opts []string, goos string) error {
	for _, o := range opts {
		name, _, _ := strings.Cut(o, "=") // idmap=uids=...
		if !podmanMountOptions[name] {
			return fmt.Errorf("podman has no mount option %q", o)
		}
		if name == "O" && goos == "freebsd" {
			return fmt.Errorf("the overlay option O is not supported on FreeBSD")
		}
	}
	if slices.Contains(opts, "ro") && slices.Contains(opts, "rw") {
		return fmt.Errorf("a mount cannot be both ro and rw")
	}
	if slices.Contains(opts, "z") && slices.Contains(opts, "Z") {
		return fmt.Errorf("a mount is labelled z (shared) or Z (private), not both")
	}
	return nil
}

// WithReadOnly is opts with ro added when readOnly asks for it.
func WithReadOnly(opts []string, readOnly bool) []string {
	if readOnly && !slices.Contains(opts, "ro") {
		return append(slices.Clone(opts), "ro")
	}
	return opts
}

// mountEntry is a short-form mount line: "src:dst[:opts]".
func mountEntry(src, dst string, opts []string) string {
	if len(opts) == 0 {
		return src + ":" + dst
	}
	return src + ":" + dst + ":" + strings.Join(opts, ",")
}

// SetMountOptions replaces the options of service's short-form mount at
// containerPath, keeping its source as written (a ${VAR} stays one).
func SetMountOptions(composeYAML, service, containerPath string, opts []string) (string, error) {
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
	if vols := mapGet(svc, "volumes"); vols != nil && vols.Kind == yaml.SequenceNode {
		for _, it := range vols.Content {
			if it.Kind != yaml.ScalarNode || mountDest(it.Value) != containerPath {
				continue
			}
			src, _, _ := strings.Cut(it.Value, ":")
			*it = *quotedScalar(mountEntry(src, containerPath, opts))
			return encodeRoot(root)
		}
	}
	return "", fmt.Errorf("%s has nothing mounted at %s", service, containerPath)
}
