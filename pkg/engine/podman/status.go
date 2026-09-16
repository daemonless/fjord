package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/stack"
)

// libpodContainer is the subset of the libpod /containers/json response we need.
type libpodContainer struct {
	ID       string   `json:"Id"`
	Names    []string `json:"Names"`
	State    string   `json:"State"`
	Networks []string `json:"Networks"`
	Ports    []struct {
		HostPort      int    `json:"host_port"`
		ContainerPort int    `json:"container_port"`
		Protocol      string `json:"protocol"`
	} `json:"Ports"`
}

// Status queries the libpod REST API over the podman unix socket for
// containers labeled with this stack's podman-compose project. The project
// name matches podman-compose's own default derivation -- dir_basename
// lowercased (see podman_compose.py norm_re / project_name handling) --
// which is safe here because stack names are already restricted to
// [a-zA-Z0-9_-] by stack.validName.
func (b *Backend) Status(ctx context.Context, s *stack.Stack) (engine.StackStatus, error) {
	containers, err := b.listStackContainers(ctx, s.Name)
	if err != nil {
		// podman_service isn't running or the socket is unreachable -- report
		// unknown rather than failing the whole stack list.
		return engine.StackStatus{State: "unknown"}, nil
	}
	return aggregateStatus(containers), nil
}

// listStackContainers returns all containers (running or not) labeled with the
// stack's podman-compose project.
func (b *Backend) listStackContainers(ctx context.Context, name string) ([]libpodContainer, error) {
	project := strings.ToLower(name)
	filters, err := json.Marshal(map[string][]string{
		"label": {"io.podman.compose.project=" + project},
	})
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("all", "true")
	q.Set("filters", string(filters))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/containers/json?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("libpod containers/json: unexpected status %s", resp.Status)
	}
	var containers []libpodContainer
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, fmt.Errorf("decode libpod response: %w", err)
	}
	return containers, nil
}

// UsedPorts maps "port/proto" -> container name for every container's
// published host ports (running or created -- a created container will grab
// its ports on start, so it counts as a conflict too).
func (b *Backend) UsedPorts(ctx context.Context) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/containers/json?all=true", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("libpod containers/json: unexpected status %s", resp.Status)
	}
	var containers []libpodContainer
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, c := range containers {
		// Only containers that actually hold the host port: running (bound now) or
		// created (about to bind on start). Exited/stopped/dead containers have
		// released their ports -- counting them yields false "port in use"
		// conflicts from stale/orphan containers (e.g. one that exited long ago).
		if c.State != "running" && c.State != "created" && c.State != "configured" {
			continue
		}
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		for _, p := range c.Ports {
			if p.HostPort > 0 {
				proto := p.Protocol
				if proto == "" {
					proto = "tcp"
				}
				out[fmt.Sprintf("%d/%s", p.HostPort, proto)] = name
			}
		}
	}
	return out, nil
}

// containerNames extracts the (de-slashed) names from a container list.
func containerNames(cs []libpodContainer) []string {
	var names []string
	for _, c := range cs {
		if len(c.Names) > 0 {
			names = append(names, strings.TrimPrefix(c.Names[0], "/"))
		}
	}
	return names
}

func aggregateStatus(containers []libpodContainer) engine.StackStatus {
	out := engine.StackStatus{Containers: make([]engine.ContainerStatus, 0, len(containers))}
	if len(containers) == 0 {
		out.State = "stopped"
		return out
	}

	running := 0
	for _, c := range containers {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		var ports []engine.Port
		for _, p := range c.Ports {
			if p.HostPort > 0 {
				ports = append(ports, engine.Port{HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: p.Protocol})
			}
		}
		out.Containers = append(out.Containers, engine.ContainerStatus{
			Name: name, State: c.State, Ports: ports, Address: containerAddress(c),
		})
		if c.State == "running" {
			running++
		}
	}

	switch {
	case running == len(containers):
		out.State = "running"
	case running == 0:
		out.State = "stopped"
	default:
		out.State = "partial"
	}
	return out
}

// containerAddress reports the address a container holds on an attachable
// network, or "" when it has none.
//
// It is read from CNI's IPAM state rather than from podman: on FreeBSD podman
// does not parse a third-party plugin's conflist, so its own view of an epair
// network carries no address at all. The compose only records one when the
// user pinned it, which leaves every auto-assigned stack with no address to
// report -- and nothing to build a working link from.
func containerAddress(c libpodContainer) string {
	if c.ID == "" {
		return ""
	}
	for _, n := range c.Networks {
		if addr := hostnet.AddressOf(n, c.ID); addr != "" {
			return addr
		}
	}
	return ""
}
