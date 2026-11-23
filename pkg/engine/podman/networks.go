package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

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
	backend := b.networkBackend(ctx)
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
	// Drop the ones made for another engine. podman loads every conflist it
	// finds, so without this a network filled in for appjail is offered here
	// too -- and its addresses come from a place podman was not asked about.
	kept := nets[:0]
	for _, n := range nets {
		if d, ok := hostnet.Get(n.Name); ok && d.For != "" && d.For != "podman" {
			continue
		}
		kept = append(kept, n)
	}
	nets = kept
	// Backfill what libpod could not tell us from the network's own config.
	for i, n := range nets {
		d, ok := hostnet.Get(n.Name)
		if !ok {
			continue
		}
		if n.Subnet == "" {
			nets[i].Subnet, nets[i].Gateway = d.Subnet, d.Gateway
		}
		// Under netavark podman calls a LAN network a bridge network; it is
		// the same kind of network the epair plugin made, so it reads the same.
		if d.Type == epairPlugin {
			nets[i].Driver = epairPlugin
		}
	}
	// A network whose plugin is gone still lists: the runtime reads the
	// conflist and does not check that the binary behind "type" exists. It
	// looks healthy right up to the moment a container fails to start on it,
	// so say it here instead.
	if backend == backendCNI && !pluginInstalled() {
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
		if seen[d.Name] || (d.For != "" && d.For != "podman") {
			continue
		}
		// A network made under the CNI backend that podman, now on netavark,
		// no longer has: a stack still names it and cannot start. Not the
		// ones compose made per project; compose makes those again.
		if d.Legacy && d.Type != epairPlugin {
			if !d.Compose && d.Name != defaultBridge {
				nets = append(nets, engine.Network{
					Name: d.Name, Driver: d.Type, Subnet: d.Subnet, Gateway: d.Gateway,
					Problem: legacyProblem + b.remakeHint(d.Name),
					Remake:  true,
				})
			}
			continue
		}
		if d.Type != epairPlugin {
			continue
		}
		row := engine.Network{Name: d.Name, Driver: epairPlugin, Subnet: d.Subnet, Gateway: d.Gateway, Problem: networkProblem()}
		switch {
		case d.Legacy && d.DHCP && !netavarkPlugin():
			row.Problem = legacyProblem + ". " + netavarkDHCPNote
		case d.Legacy:
			row.Problem = legacyProblem + b.remakeHint(d.Name)
			row.Remake = true
		}
		nets = append(nets, row)
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

// legacyProblem is a LAN network made for podman's CNI backend on a host that
// has since moved to netavark.
const legacyProblem = "podman no longer loads this network: it was made for the CNI network backend and podman here now uses netavark"

// networkProblem explains why the runtime ignored a network fjord can see.
func networkProblem() string {
	if !pluginInstalled() {
		return "podman cannot load this network: the " + epairPlugin + " plugin is not installed at /usr/local/libexec/cni/" + epairPlugin
	}
	return "podman did not load this network; check its config"
}

// remakeHint ends a legacy network's problem line: why fjord could not make
// it again by itself, when it tried, and what to do.
func (b *Backend) remakeHint(name string) string {
	if why := b.carryNote(name); why != "" {
		if rest, ok := strings.CutPrefix(why, engine.ErrInUse.Error()+": "); ok {
			users, _, _ := strings.Cut(rest, " still runs")
			verb := " still runs"
			if strings.Contains(users, ", ") {
				verb = " still run"
			}
			return ". " + users + verb + " on it as it was and keeps working. fjord makes the network again once nothing runs on it: restart the stacks that use it, or reboot"
		}
		return ". fjord could not make it again by itself: " + why + ". Fix that, then Make it again"
	}
	return ". Make it again so its stacks can start"
}
