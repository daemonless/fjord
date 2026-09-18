package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
)

// libpodNetwork is the subset of the libpod /networks/json response we need.
type libpodNetwork struct {
	Name    string `json:"name"`
	Driver  string `json:"driver"`
	Subnets []struct {
		Subnet  string `json:"subnet"`
		Gateway string `json:"gateway"`
	} `json:"subnets"`
	Labels map[string]string `json:"labels"`
}

// composeProject marks a network compose made for one project, rather than one
// someone created to attach stacks to. compose creates "<project>_default" per
// stack and labels it; the label is the reliable tell, since the name is only
// a convention and a user may legitimately have a network called that.
func (n libpodNetwork) composeProject() bool {
	for _, k := range []string{"com.docker.compose.project", "io.podman.compose.project"} {
		if n.Labels[k] != "" {
			return true
		}
	}
	return false
}

// Networks lists the attachable networks over the libpod socket: the ones that
// give a container its own routable IP on the LAN, which is what lets a stack
// bind :80/:443 without colliding on the host's ports. Bridge/default networks
// don't solve that, so they're omitted.
func (b *Backend) Networks(ctx context.Context) ([]engine.Network, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/networks/json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("libpod networks/json: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("libpod networks/json: unexpected status %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	nets, err := parseNetworks(data)
	if err != nil {
		return nil, err
	}
	// Backfill what libpod could not tell us from the network's own config.
	for i, n := range nets {
		if n.Subnet == "" {
			if d, ok := hostnet.Get(n.Name); ok {
				nets[i].Subnet, nets[i].Gateway = d.Subnet, d.Gateway
			}
		}
	}
	// A network whose plugin is gone still lists: the runtime reads the
	// conflist and does not check that the binary behind "type" exists. It
	// looks healthy right up to the moment a container fails to start on it,
	// so say it here instead.
	if !pluginInstalled() {
		for i, n := range nets {
			if n.Driver == epairPlugin && n.Problem == "" {
				nets[i].Problem = networkProblem()
			}
		}
	}
	// A conflist the runtime rejected -- typically a plugin that is not
	// installed -- never appears in its list. Add it anyway: an invisible
	// network is one the user cannot delete either.
	// Compare against every name the runtime reported, not the attachable
	// subset: a project's bridge network is filtered out on purpose and is not
	// missing.
	var all []struct {
		Name string `json:"name"`
	}
	json.Unmarshal(data, &all)
	seen := map[string]bool{}
	for _, n := range all {
		seen[n.Name] = true
	}
	for _, d := range hostnet.List() {
		// Only this plugin's networks: a conflist for something else is not
		// fjord's to explain.
		if seen[d.Name] || d.Type != epairPlugin {
			continue
		}
		nets = append(nets, engine.Network{
			Name: d.Name, Driver: epairPlugin, Subnet: d.Subnet, Gateway: d.Gateway,
			Problem: networkProblem(),
		})
	}
	// Attachments, so the UI can refuse to delete a network in use.
	if users, err := b.networkUsersAll(ctx); err == nil {
		for i, n := range nets {
			nets[i].UsedBy = users[n.Name]
		}
	}
	return nets, nil
}

// hostScopedDrivers are the drivers whose networks do NOT get a container its
// own LAN address, so they are never offered as an attachment.
var hostScopedDrivers = map[string]bool{
	"host": true, "none": true, "null": true,
}

// defaultBridge is the network the runtime makes for itself. Every stack that
// asks for nothing is already on it, so offering it as a choice is noise --
// as is the one compose makes per project (see composeProject).
const defaultBridge = "podman"

// parseNetworks filters the libpod network list to attachable networks.
// Split out from the HTTP call so it can be unit-tested without a socket.
//
// Matched by what the network DOES (a subnet of its own, not host-scoped)
// rather than by driver name: the driver is "macvlan" on Linux but "epair" on
// FreeBSD, where there is no macvlan and a bridge + epair pair stands in for
// it. Keying on the name left every FreeBSD host with an empty network picker.
func parseNetworks(data []byte) ([]engine.Network, error) {
	var raw []libpodNetwork
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode libpod networks: %w", err)
	}
	out := make([]engine.Network, 0, len(raw))
	for _, n := range raw {
		if hostScopedDrivers[n.Driver] || n.Name == defaultBridge || n.composeProject() {
			continue
		}
		net := engine.Network{Name: n.Name, Driver: n.Driver}
		// A subnet is NOT required here. On FreeBSD libpod reports only name
		// and driver for a third-party CNI plugin's network -- it never parses
		// the conflist -- so requiring one drops every epair network. Networks
		// fills the gap by reading the conflist itself.
		if len(n.Subnets) > 0 {
			net.Subnet = n.Subnets[0].Subnet
			net.Gateway = n.Subnets[0].Gateway
		}
		out = append(out, net)
	}
	return out, nil
}

// networkProblem explains why the runtime ignored a network fjord can see.
func networkProblem() string {
	if !pluginInstalled() {
		return "podman cannot load this network: the " + epairPlugin + " plugin is not installed at /usr/local/libexec/cni/" + epairPlugin
	}
	return "podman did not load this network; check its config"
}
