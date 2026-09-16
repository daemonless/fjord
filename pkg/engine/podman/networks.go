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
	"bridge": true, "host": true, "none": true, "null": true,
}

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
		if hostScopedDrivers[n.Driver] {
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
