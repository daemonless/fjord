package compose

import (
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Service is one compose service broken out for engines that run services
// individually (e.g. appjail, which has no compose executor of its own). All
// ${VAR} references are resolved against env.
type Service struct {
	Name        string
	Image       string
	Env         map[string]string
	Ports       []PortMap
	Volumes     []VolMount
	NetworkHost bool     // network_mode: host
	DependsOn   []string // services that must start first
	Annotations map[string]string
}

// PortMap is one published port of a service.
type PortMap struct {
	Host      int
	Container int
	Proto     string // "tcp" | "udp"
}

// VolMount is one mount of a service: a host-path bind, or a named volume.
type VolMount struct {
	// Source is the host path of a bind mount, empty for a named volume.
	// Callers that need somewhere on this host -- the appjail bundle writer
	// materialising nullfs mounts -- key on it being non-empty.
	Source string
	// Name is the volume's name, empty for a bind mount. Named volumes used to
	// be dropped here entirely, which left a service whose only storage is one
	// (immich's model cache, redis's data) reporting no storage at all.
	Name     string
	Dest     string
	ReadOnly bool
}

// ParseServices returns every compose service with its image, env, ports,
// bind-mount volumes, host-network flag and jail annotations -- everything a
// non-compose engine needs to run the service itself.
func ParseServices(composeYAML string, env map[string]string) []Service {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	services := mapGet(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil
	}
	resolve := func(s string) string { return ExpandEnv(strings.Trim(s, `"`), env) }

	var out []Service
	for i := 0; i+1 < len(services.Content); i += 2 {
		name := services.Content[i].Value
		svc := services.Content[i+1]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		s := Service{Name: name, Env: map[string]string{}, Annotations: map[string]string{}}

		if img := mapGet(svc, "image"); img != nil {
			s.Image = resolve(img.Value)
		}
		if nm := mapGet(svc, "network_mode"); nm != nil && resolve(nm.Value) == "host" {
			s.NetworkHost = true
		}

		// env_file: load the named files' KEY=VALUE lines as base env. The
		// common case is `.env`, whose vars are already in the resolved env
		// map, so merge that; explicit `environment:` below overrides.
		if ef := mapGet(svc, "env_file"); ef != nil {
			for k, v := range env {
				s.Env[k] = v
			}
		}

		// depends_on: list of service names (or a map of them).
		if d := mapGet(svc, "depends_on"); d != nil {
			switch d.Kind {
			case yaml.SequenceNode:
				for _, it := range d.Content {
					s.DependsOn = append(s.DependsOn, it.Value)
				}
			case yaml.MappingNode:
				for j := 0; j+1 < len(d.Content); j += 2 {
					s.DependsOn = append(s.DependsOn, d.Content[j].Value)
				}
			}
		}

		// environment: list ("- KEY=val") or map (KEY: val).
		if e := mapGet(svc, "environment"); e != nil {
			switch e.Kind {
			case yaml.SequenceNode:
				for _, it := range e.Content {
					kv := resolve(it.Value)
					if eq := strings.IndexByte(kv, '='); eq > 0 {
						s.Env[kv[:eq]] = kv[eq+1:]
					}
				}
			case yaml.MappingNode:
				for j := 0; j+1 < len(e.Content); j += 2 {
					s.Env[e.Content[j].Value] = resolve(e.Content[j+1].Value)
				}
			}
		}

		// annotations: map of key -> value (jail params live here).
		if a := mapGet(svc, "annotations"); a != nil && a.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(a.Content); j += 2 {
				s.Annotations[a.Content[j].Value] = resolve(a.Content[j+1].Value)
			}
		}

		// ports: "host:container[/proto]".
		if p := mapGet(svc, "ports"); p != nil && p.Kind == yaml.SequenceNode {
			for _, it := range p.Content {
				if pm, ok := parsePort(resolve(it.Value)); ok {
					s.Ports = append(s.Ports, pm)
				}
			}
		}

		// volumes: "src:dst[:opts]" -- src an absolute host path (a bind) or a
		// volume name. Both are storage the service holds; only the container
		// side has to be absolute for either to mean anything.
		if v := mapGet(svc, "volumes"); v != nil && v.Kind == yaml.SequenceNode {
			for _, it := range v.Content {
				parts := strings.Split(resolve(it.Value), ":")
				if len(parts) < 2 || !strings.HasPrefix(parts[1], "/") {
					continue
				}
				var vm VolMount
				switch {
				case strings.HasPrefix(parts[0], "/"):
					vm = VolMount{Source: parts[0], Dest: parts[1]}
				case volumeNameRe.MatchString(parts[0]):
					vm = VolMount{Name: parts[0], Dest: parts[1]}
				default:
					continue
				}
				if len(parts) >= 3 && strings.Contains(parts[2], "ro") {
					vm.ReadOnly = true
				}
				s.Volumes = append(s.Volumes, vm)
			}
		}
		out = append(out, s)
	}
	return out
}

// volumeNameRe is what compose allows a named volume to be called. Anchored so
// a half-resolved "${VAR}" or a relative path is not mistaken for one.
var volumeNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// parsePort parses "[ip:]host:container[/proto]" (or a bare "port"); an IPv6
// bind address is bracketed, "[::1]:8080:80".
func parsePort(spec string) (PortMap, bool) {
	proto := "tcp"
	if s := strings.IndexByte(spec, '/'); s >= 0 {
		proto = spec[s+1:]
		spec = spec[:s]
	}
	host, cont := SplitPortSpec(spec)
	h, err1 := strconv.Atoi(host)
	c, err2 := strconv.Atoi(cont)
	if err1 != nil || err2 != nil || h <= 0 {
		return PortMap{}, false
	}
	return PortMap{Host: h, Container: c, Proto: proto}, true
}

// SplitPortSpec returns the host and container ports of a compose port spec
// without its protocol: "8080:80" -> 8080,80; "127.0.0.1:8080:80" -> 8080,80;
// "[::1]:8080:80" -> 8080,80; "80" -> 80,80.
func SplitPortSpec(spec string) (host, cont string) {
	if strings.HasPrefix(spec, "[") {
		if i := strings.Index(spec, "]:"); i >= 0 {
			spec = spec[i+2:]
		}
	}
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 1:
		return parts[0], parts[0]
	case 2:
		return parts[0], parts[1]
	default:
		return parts[len(parts)-2], parts[len(parts)-1]
	}
}

// WithDependents is services plus every service that depends on one of them,
// directly or through another, in compose order.
//
// podman-compose records depends_on as a podman dependency, and podman will
// not remove a container something else requires: recreating immich's
// database on its own fails with "has dependent containers", leaving the old
// one running. So a service is only ever recreated together with what needs
// it -- which restarts those anyway, since they lose it for the duration.
func WithDependents(composeYAML string, services []string) []string {
	all := ParseServices(composeYAML, nil)
	in := map[string]bool{}
	for _, s := range services {
		in[s] = true
	}
	for grew := true; grew; {
		grew = false
		for _, s := range all {
			if in[s.Name] {
				continue
			}
			for _, d := range s.DependsOn {
				if in[d] {
					in[s.Name], grew = true, true
					break
				}
			}
		}
	}
	out := make([]string, 0, len(in))
	for _, s := range all {
		if in[s.Name] {
			out = append(out, s.Name)
		}
	}
	return out
}
