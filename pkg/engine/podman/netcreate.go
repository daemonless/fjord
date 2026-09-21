package podman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/lannet"
)

// epairPlugin is the CNI type FreeBSD networks use: sysutils/cni-epair, which
// makes an epair per container and puts one end on the parent bridge. FreeBSD
// has no macvlan, and `podman network create` validates --driver against its
// own list (bridge/macvlan/ipvlan) and so cannot express this -- which is why
// the conflist is written directly rather than shelling out to podman.
const epairPlugin = "epair"

// NetworkKinds is podman's contribution to Capabilities: what it can create
// on this host. A private network works anywhere podman does; a LAN network
// needs the epair plugin, so it is FreeBSD-only (on Linux the mechanism is
// kernel macvlan, a different code path).
func (b *Backend) networkKinds() []engine.NetworkKind {
	// A private network is just `podman network create`: no plugin, no
	// bridge of the user's, nothing platform-specific. It is offered
	// wherever podman runs, so the two engines describe the same two
	// choices instead of one each.
	kinds := []engine.NetworkKind{{
		ID:                  "nat",
		Label:               "bridge",
		Help:                "The engine creates the bridge and hands out addresses. Containers reach the outside through the host; nothing on your LAN can reach them directly.",
		AddressNote:         "Addresses come from this host: a segment it invents has no DHCP server to ask.",
		SupportsMTU:         true,
		SupportsDescription: false,
	}}
	if runtime.GOOS != "freebsd" || !pluginInstalled() {
		return kinds
	}
	// The LAN kind is the host's, not podman's -- podman only knows whether
	// its own plugin can take a DHCP lease.
	return append([]engine.NetworkKind{lannet.Kind(pluginSupports("dhcp"))}, kinds...)
}

// CreateNetwork writes the network's conflist. podman picks it up by reading
// the directory, so there is nothing to restart.
func (b *Backend) CreateNetwork(ctx context.Context, spec engine.NetworkSpec) (engine.Network, error) {
	if len(b.networkKinds()) == 0 {
		// networkNote knows every reason -- wrong platform, missing plugin --
		// so it cannot go stale the way a hand-written message did.
		return engine.Network{}, errors.New(b.networkNote())
	}
	if spec.Kind == "nat" {
		return b.createNAT(ctx, spec)
	}
	if spec.Kind != "" && spec.Kind != "lan" {
		return engine.Network{}, fmt.Errorf("unknown network kind %q", spec.Kind)
	}
	if spec.AddressSource == "static" && !cniPlugin("static") {
		return engine.Network{}, errors.New("the CNI static IPAM plugin is not installed at /usr/local/libexec/cni/static, so a network that assigns no addresses cannot be created here")
	}
	if spec.AddressSource == "dhcp" && !pluginSupports("dhcp") {
		return engine.Network{}, fmt.Errorf("the installed %s plugin cannot do DHCP; upgrade sysutils/cni-epair or choose a pool", epairPlugin)
	}
	existing, err := b.Networks(ctx)
	if err == nil {
		for _, n := range existing {
			if n.Name == spec.Name {
				return engine.Network{}, fmt.Errorf("a network named %q already exists", spec.Name)
			}
		}
	}
	return lannet.Create(ctx, spec)
}

// RemoveNetwork deletes the network's conflist. Unlike a podman-managed
// network, nothing stops the delete on its own: containers already attached
// keep running with an address the plugin can no longer resolve on teardown,
// so attachments are checked here first.
// createNAT makes a private network the runtime owns: its own bridge, its own
// subnet, NAT out through the host. `podman network create` does all of it,
// including picking a free range when none is given -- the same shape appjail
// offers, so the choice on the form is what kind of network it is rather than
// which engine is making it.
func (b *Backend) createNAT(ctx context.Context, spec engine.NetworkSpec) (engine.Network, error) {
	if !hostnet.NameRe.MatchString(spec.Name) {
		return engine.Network{}, fmt.Errorf("invalid network name %q: letters, digits, dot, dash and underscore only", spec.Name)
	}
	args := []string{"network", "create"}
	if spec.Subnet != "" {
		ip, _, err := net.ParseCIDR(spec.Subnet)
		if err != nil {
			return engine.Network{}, fmt.Errorf("invalid subnet %q: want a CIDR like 10.100.0.0/24", spec.Subnet)
		}
		// The runtime claims the first address as the gateway. On a segment
		// that already exists that address is the router's.
		if !ip.IsPrivate() {
			return engine.Network{}, fmt.Errorf("%s is not a private range: a network the engine creates must be one it owns (10.x, 172.16-31.x, 192.168.x)", spec.Subnet)
		}
		args = append(args, "--subnet", spec.Subnet)
	}
	if spec.Gateway != "" {
		// Checked here, not just at the UI: podman's own error for a malformed
		// gateway is a bare parse dump, and the subnet right above it is
		// validated -- an unvalidated gateway next to it reads as an oversight.
		if net.ParseIP(spec.Gateway) == nil {
			return engine.Network{}, fmt.Errorf("invalid gateway %q: want an IP address like 10.100.0.1", spec.Gateway)
		}
		args = append(args, "--gateway", spec.Gateway)
	}
	if spec.MTU > 0 {
		args = append(args, "--opt", "mtu="+strconv.Itoa(spec.MTU))
	}
	args = append(args, spec.Name)
	if out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput(); err != nil {
		return engine.Network{}, fmt.Errorf("podman network create: %s", strings.TrimSpace(string(out)))
	}
	n := engine.Network{Name: spec.Name, Driver: "bridge", Subnet: spec.Subnet, Gateway: spec.Gateway}
	if def, ok := hostnet.Get(spec.Name); ok && n.Subnet == "" {
		n.Subnet, n.Gateway = def.Subnet, def.Gateway
	}
	return n, nil
}

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
	// A network the runtime made is the runtime's to remove: it holds state
	// beyond the conflist (the bridge, the IPAM directory). One it never
	// parsed -- a third-party plugin's -- is only the file.
	if def, ok := hostnet.Get(name); ok && def.Type != lannet.Plugin {
		if out, err := exec.CommandContext(ctx, "podman", "network", "rm", "-f", name).CombinedOutput(); err != nil {
			return fmt.Errorf("podman network rm: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
	return lannet.Remove(name)
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
	return lannet.Parents(ctx)
}

// ParentSetup re-renders one setup with the user's choices. Implements
// engine.NetworkSetupper.
func (b *Backend) ParentSetup(ctx context.Context, kind, nic, vlan string) (engine.ParentSetup, error) {
	if kind != "lan" {
		return engine.ParentSetup{}, fmt.Errorf("network kind %q takes no parent", kind)
	}
	if nic != "" && !ifaceNameRe.MatchString(nic) {
		return engine.ParentSetup{}, fmt.Errorf("invalid interface name %q", nic)
	}
	if vlan != "" && vlan != "auto" {
		n, err := strconv.Atoi(vlan)
		if err != nil || n < 1 || n > 4094 {
			return engine.ParentSetup{}, fmt.Errorf("VLAN id must be a number from 1 to 4094")
		}
	}
	return lannet.ParentSetup(nic, vlan)[0], nil
}

// ifaceNameRe bounds a name that lands inside a shell snippet the user pastes
// as root; only what ifconfig would accept gets through.
var ifaceNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._]{0,14}$`)

// pluginSupports asks the installed plugin whether it can do something beyond
// the CNI contract. A plugin that predates the FEATURES command simply fails,
// which is the right answer: it cannot.
func pluginSupports(feature string) bool {
	cmd := exec.Command(filepath.Join("/usr/local/libexec/cni", epairPlugin))
	cmd.Env = append(os.Environ(), "CNI_COMMAND=FEATURES")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(ln) == feature {
			return true
		}
	}
	return false
}

// networkNote explains an empty kind list. A missing plugin is the common one
// and is fixable, so it says how: writing a conflist for a plugin that is not
// installed produces a network that accepts containers and then fails every
// one of them at start.
func (b *Backend) networkNote() string {
	if runtime.GOOS != "freebsd" {
		return "Creating networks is only supported on FreeBSD hosts; on Linux use podman network create."
	}
	if !pluginInstalled() {
		// Deliberately not just "pkg install cni-epair": the port is not in
		// the tree yet, so that command fails and the reader concludes fjord
		// is wrong rather than early.
		return "Giving containers their own address needs the " + epairPlugin + " CNI plugin, which is not installed. It is not in the ports tree yet -- fetch it to /usr/local/libexec/cni/" + epairPlugin + " from github.com/daemonless/cni-epair and chmod 755 it, or once the port lands, pkg install cni-epair."
	}
	return ""
}

func pluginInstalled() bool { return cniPlugin(epairPlugin) }

// cniPlugin reports whether a CNI plugin binary is present. A conflist naming
// one that is not there loads fine and fails when a container starts on it.
func cniPlugin(name string) bool {
	_, err := os.Stat(filepath.Join("/usr/local/libexec/cni", name))
	return err == nil
}
