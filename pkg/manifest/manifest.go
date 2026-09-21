// Package manifest parses x-fjord catalog manifests (a compose file plus an
// x-fjord metadata block) and resolves a user's wizard input into the .env
// values and host directories needed to render a runnable stack.
package manifest

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Var is a wizard variable declared under x-fjord.variables.
type Var struct {
	Name     string
	Type     string // port | string | secret | path | zfs_dataset
	Default  string
	Optional bool
	Uid, Gid int    // host_permissions for zfs_dataset (0 -> default 1000)
	Mode     string // octal, e.g. "755"
}

// Manifest is a parsed x-fjord manifest: the compose half (x-fjord stripped,
// ${VAR} placeholders intact) plus the variable declarations. WebPort/WebHTTPS
// carry the catalog's web-endpoint hint (from the image's cit config) -- may be
// a literal port or a "${VAR}" reference resolved from the stack's .env.
type Manifest struct {
	compose   string
	appjail   *AppjailBundle
	Variables []Var
	WebPort   string
	WebHTTPS  bool
	// Networking is the app's own answer to "which service goes where": a map
	// of service name to a network SPEC, with "*" standing for every service
	// not named. The specs are Default (whatever the install was told to use)
	// and Private (a segment only this stack can reach); anything else is a
	// network name to use as it stands.
	//
	// A stack knows which of its services is the one people open and which are
	// its database and cache; the person installing it does not, and should
	// not have to say. Without this, putting immich on a network put its
	// postgres on that network too.
	Networking map[string]string
	// Hostnames maps a service to the environment variable that carries its
	// address for the rest of the stack: immich's database -> DB_HOSTNAME.
	//
	// With container DNS a service is reachable at its own name, so fjord
	// writes the NAME, not an address -- no pinning, no allocation, nothing
	// to decide at install. The bundle keeps localhost as its default, which
	// is right for a stack that shares one network stack and wrong the moment
	// its parts are given their own.
	Hostnames map[string]string
}

// Network specs a manifest may give a service.
const (
	// NetworkDefault is what the install was told to use.
	NetworkDefault = "default"
	// NetworkPrivate is a segment only this stack's own services can reach.
	NetworkPrivate = "private"
	// NetworkEveryOther is the key standing for every service not named.
	NetworkEveryOther = "*"
)

// AppjailBundle is the dbuild-rendered AppJail deploy bundle carried inline in
// the manifest under x-fjord.appjail. The appjail engine runs these verbatim
// via appjail-director instead of translating the compose itself; fjord fills
// the placeholder .env (WEB_PORT + one var per volume device) at install.
type AppjailBundle struct {
	Director     string `yaml:"director"`
	Makejail     string `yaml:"makejail"`
	TemplateConf string `yaml:"template_conf"`
	EnvDefaults  string `yaml:"env_defaults"`
	// Extras are further bundle files (a sidecar's own jail template),
	// written next to director.yml under their own names.
	Extras map[string]string `yaml:"extras"`
}

// Compose returns the compose YAML with the x-fjord block removed and the
// ${VAR} placeholders left in place (podman-compose substitutes them from .env).
func (m *Manifest) Compose() string { return m.compose }

// WebContainerPort is the port the web UI listens on inside the container:
// the container side of the ports entry published as WebPort. WebPort is a
// host-side value (usually "${WEB_PORT}"), which is meaningless for a stack
// on a macvlan address, where the app answers on its own port. Falls back to
// the variable's default, then a literal WebPort; "" when unknown.
func (m *Manifest) WebContainerPort() string {
	if m.WebPort == "" {
		return ""
	}
	re := regexp.MustCompile(`(?m)^\s*-\s*["']?` + regexp.QuoteMeta(m.WebPort) + `:(\d{2,5})(?:/tcp)?["']?\s*$`)
	if mm := re.FindStringSubmatch(m.compose); mm != nil {
		return mm[1]
	}
	if name := strings.TrimSuffix(strings.TrimPrefix(m.WebPort, "${"), "}"); name != m.WebPort {
		for _, v := range m.Variables {
			if v.Name == name && isPort(v.Default) {
				return v.Default
			}
		}
		return ""
	}
	if isPort(m.WebPort) {
		return m.WebPort
	}
	return ""
}

func isPort(s string) bool {
	if len(s) < 2 || len(s) > 5 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Appjail returns the AppJail director bundle, or nil when the app declares no
// appjail support (appjail: false, or no bundle was rendered at catalog time).
func (m *Manifest) Appjail() *AppjailBundle { return m.appjail }

// xfVar mirrors the on-disk x-fjord variable shape for decoding.
type xfVar struct {
	Name            string `yaml:"name"`
	Type            string `yaml:"type"`
	Default         string `yaml:"default"`
	Optional        bool   `yaml:"optional"`
	HostPermissions *struct {
		Uid  int    `yaml:"uid"`
		Gid  int    `yaml:"gid"`
		Mode string `yaml:"mode"`
	} `yaml:"host_permissions"`
}

// Parse splits a manifest into its compose half and its variables.
func Parse(manifestYAML string) (*Manifest, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(manifestYAML), &doc); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("manifest is not a YAML mapping")
	}
	root := doc.Content[0]

	// Split x-fjord out of the top-level mapping; keep everything else as compose.
	var xfNode *yaml.Node
	kept := make([]*yaml.Node, 0, len(root.Content))
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if k.Value == "x-fjord" {
			xfNode = v
			continue
		}
		// Drop the top-level compose project name (the manifest's app id); the
		// stack dir name becomes the project, so status filters + container
		// names track the stack, not the app id.
		if k.Value == "name" {
			continue
		}
		kept = append(kept, k, v)
	}
	if xfNode == nil {
		return nil, fmt.Errorf("manifest has no x-fjord section")
	}
	root.Content = kept

	// Catalog manifests hardcode container_name (e.g. "radarr"), which forces a
	// fixed name -- colliding with any existing container of that name and
	// preventing a second instance. Strip it so each install is self-contained;
	// podman-compose then names the container <stack>_<service>_1. Service-name
	// network aliases are unaffected.
	stripContainerNames(root)

	var xf struct {
		Info struct {
			WebPort  string `yaml:"web_port"`
			WebHTTPS bool   `yaml:"web_https"`
		} `yaml:"info"`
		Variables  []xfVar           `yaml:"variables"`
		Appjail    *AppjailBundle    `yaml:"appjail"`
		Networking map[string]string `yaml:"networking"`
		Hostnames  map[string]string `yaml:"hostnames"`
	}
	if err := xfNode.Decode(&xf); err != nil {
		return nil, fmt.Errorf("decode x-fjord: %w", err)
	}
	vars := make([]Var, 0, len(xf.Variables))
	for _, v := range xf.Variables {
		nv := Var{Name: v.Name, Type: v.Type, Default: v.Default, Optional: v.Optional}
		if v.HostPermissions != nil {
			nv.Uid, nv.Gid, nv.Mode = v.HostPermissions.Uid, v.HostPermissions.Gid, v.HostPermissions.Mode
		}
		vars = append(vars, nv)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	enc.Close()

	return &Manifest{compose: buf.String(), appjail: xf.Appjail, Variables: vars,
		WebPort: xf.Info.WebPort, WebHTTPS: xf.Info.WebHTTPS,
		Networking: xf.Networking, Hostnames: xf.Hostnames}, nil
}

// stripContainerNames removes container_name from every service mapping.
func stripContainerNames(root *yaml.Node) {
	services := childByKey(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return
	}
	for i := 1; i < len(services.Content); i += 2 {
		if svc := services.Content[i]; svc.Kind == yaml.MappingNode {
			removeKey(svc, "container_name")
		}
	}
}

func childByKey(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func removeKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}
