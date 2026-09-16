package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
)

// epairPlugin is the CNI type FreeBSD networks use: sysutils/cni-epair, which
// makes an epair per container and puts one end on the parent bridge. FreeBSD
// has no macvlan, and `podman network create` validates --driver against its
// own list (bridge/macvlan/ipvlan) and so cannot express this -- which is why
// the conflist is written directly rather than shelling out to podman.
const epairPlugin = "epair"

// NetworkKinds is podman's contribution to Capabilities: what it can create
// on this host. Only FreeBSD is implemented; on Linux the mechanism is kernel
// macvlan via `podman network create`, which is a different code path.
func (b *Backend) networkKinds() []engine.NetworkKind {
	if runtime.GOOS != "freebsd" {
		return nil
	}
	return []engine.NetworkKind{{
		ID:            "lan",
		Label:         "Own IP on a bridge",
		Help:          "Containers get their own address on the segment the bridge is on, so they can bind :80/:443 without colliding with the host.",
		ParentLabel:   "Bridge",
		NeedsGateway:  true,
		SupportsMTU:   true,
		SupportsRange: true,
	}}
}

// conflist renders spec as a CNI network configuration. Split out from the
// write so it can be tested without touching the host.
func conflist(spec engine.NetworkSpec) ([]byte, error) {
	if !hostnet.NameRe.MatchString(spec.Name) {
		return nil, fmt.Errorf("invalid network name %q: letters, digits, dot, dash and underscore only", spec.Name)
	}
	if spec.Parent == "" {
		return nil, fmt.Errorf("a bridge is required")
	}
	if _, _, err := net.ParseCIDR(spec.Subnet); err != nil {
		return nil, fmt.Errorf("invalid subnet %q: want a CIDR like 192.168.4.0/24", spec.Subnet)
	}
	_, ipnet, _ := net.ParseCIDR(spec.Subnet)
	gw := net.ParseIP(spec.Gateway)
	if gw == nil {
		return nil, fmt.Errorf("invalid gateway %q", spec.Gateway)
	}
	if !ipnet.Contains(gw) {
		return nil, fmt.Errorf("gateway %s is not inside %s", spec.Gateway, spec.Subnet)
	}
	rng := map[string]any{"subnet": spec.Subnet, "gateway": spec.Gateway}
	for label, v := range map[string]string{"rangeStart": spec.RangeStart, "rangeEnd": spec.RangeEnd} {
		if v == "" {
			continue
		}
		ip := net.ParseIP(v)
		if ip == nil || !ipnet.Contains(ip) {
			return nil, fmt.Errorf("%s %s is not inside %s", label, v, spec.Subnet)
		}
		rng[label] = v
	}
	plugin := map[string]any{
		"type":   epairPlugin,
		"master": spec.Parent,
		"ipam": map[string]any{
			"type":   "host-local",
			"routes": []map[string]string{{"dst": "0.0.0.0/0"}},
			"ranges": [][]map[string]any{{rng}},
		},
		"capabilities": map[string]bool{"ips": true, "mac": true},
	}
	if spec.MTU > 0 {
		plugin["mtu"] = spec.MTU
	}
	doc := map[string]any{
		"cniVersion": "0.4.0",
		"name":       spec.Name,
		"plugins":    []any{plugin},
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// CreateNetwork writes the network's conflist. podman picks it up by reading
// the directory, so there is nothing to restart.
func (b *Backend) CreateNetwork(ctx context.Context, spec engine.NetworkSpec) (engine.Network, error) {
	if len(b.networkKinds()) == 0 {
		return engine.Network{}, fmt.Errorf("creating networks is only supported on FreeBSD hosts (this is %s); on Linux use podman network create --driver macvlan", runtime.GOOS)
	}
	if spec.Kind != "" && spec.Kind != "lan" {
		return engine.Network{}, fmt.Errorf("unknown network kind %q", spec.Kind)
	}
	data, err := conflist(spec)
	if err != nil {
		return engine.Network{}, err
	}
	existing, err := b.Networks(ctx)
	if err == nil {
		for _, n := range existing {
			if n.Name == spec.Name {
				return engine.Network{}, fmt.Errorf("a network named %q already exists", spec.Name)
			}
		}
	}
	path := hostnet.Path(spec.Name)
	if _, err := os.Stat(path); err == nil {
		return engine.Network{}, fmt.Errorf("%s already exists", path)
	}
	if err := os.MkdirAll(hostnet.ConfDir, 0o755); err != nil {
		return engine.Network{}, err
	}
	// Write-then-rename: podman reads this directory continuously, and a
	// half-written conflist is a broken network rather than an absent one.
	tmp, err := os.CreateTemp(hostnet.ConfDir, "."+spec.Name+".*.tmp")
	if err != nil {
		return engine.Network{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return engine.Network{}, err
	}
	if err := tmp.Close(); err != nil {
		return engine.Network{}, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return engine.Network{}, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return engine.Network{}, err
	}
	return engine.Network{Name: spec.Name, Driver: epairPlugin, Subnet: spec.Subnet, Gateway: spec.Gateway}, nil
}

// RemoveNetwork deletes the network's conflist. Unlike a podman-managed
// network, nothing stops the delete on its own: containers already attached
// keep running with an address the plugin can no longer resolve on teardown,
// so attachments are checked here first.
func (b *Backend) RemoveNetwork(ctx context.Context, name string, force bool) error {
	if !hostnet.NameRe.MatchString(name) {
		return fmt.Errorf("invalid network name %q", name)
	}
	path := hostnet.Path(name)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no network %q at %s", name, path)
	}
	if !force {
		users, err := b.networkUsers(ctx, name)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return fmt.Errorf("%w: %s is attached to %s", engine.ErrInUse, name, strings.Join(users, ", "))
		}
	}
	return os.Remove(path)
}

// networkUsers names the containers attached to one network.
func (b *Backend) networkUsers(ctx context.Context, name string) ([]string, error) {
	all, err := b.networkUsersAll(ctx)
	if err != nil {
		return nil, err
	}
	return all[name], nil
}

// networkUsersAll maps network name -> attached container names in a single
// libpod call, so listing N networks does not make N round trips.
func (b *Backend) networkUsersAll(ctx context.Context) (map[string][]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/containers/json?all=true", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("libpod containers/json: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("libpod containers/json: unexpected status %s", resp.Status)
	}
	var cts []struct {
		Names    []string `json:"Names"`
		Networks []string `json:"Networks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cts); err != nil {
		return nil, fmt.Errorf("decode containers: %w", err)
	}
	users := map[string][]string{}
	for _, c := range cts {
		if len(c.Names) == 0 {
			continue
		}
		for _, n := range c.Networks {
			users[n] = append(users[n], c.Names[0])
		}
	}
	for n := range users {
		sort.Strings(users[n])
	}
	return users, nil
}

// NetworkParents lists the host bridges a "lan" network can attach to. The
// runtimes' own bridges are excluded: podman's and appjail's are managed by
// those tools, and putting a LAN network on one would cross the two.
func (b *Backend) NetworkParents(ctx context.Context) ([]engine.NetworkParent, error) {
	if len(b.networkKinds()) == 0 {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, "ifconfig", "-g", "bridge").Output()
	if err != nil {
		return nil, fmt.Errorf("listing bridges: %w", err)
	}
	inUse := map[string]bool{}
	if nets, err := b.Networks(ctx); err == nil {
		for _, n := range nets {
			if d, ok := hostnet.Get(n.Name); ok && d.Bridge != "" {
				inUse[d.Bridge] = true
			}
		}
	}
	var parents []engine.NetworkParent
	for _, line := range strings.Fields(string(out)) {
		if isRuntimeBridge(line) {
			continue
		}
		parents = append(parents, engine.NetworkParent{Name: line, InUse: inUse[line]})
	}
	sort.Slice(parents, func(i, j int) bool { return parents[i].Name < parents[j].Name })
	return parents, nil
}

// isRuntimeBridge reports bridges a container runtime manages itself.
func isRuntimeBridge(name string) bool {
	return strings.HasPrefix(name, "cni-") || strings.HasPrefix(name, "podman") || name == "ajnet"
}
