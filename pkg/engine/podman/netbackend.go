package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/stack"
)

// Podman's network backends. They keep networks in different formats and
// know different things: netavark reports every container's address, CNI
// with a third-party plugin reports none.
const (
	backendCNI      = "cni"
	backendNetavark = "netavark"
)

// backendMaxAge is how long an answer is used before podman is asked again.
// The backend only changes with podman's configuration and a restart of its
// service.
const backendMaxAge = 30 * time.Second

// backendEpoch moves on when something that can change the backend has just
// happened; an answer from an earlier epoch is asked for again.
var backendEpoch atomic.Int64

// ForgetNetworkBackend makes the next caller ask podman for its backend
// again: its service was just restarted, which is when the answer changes.
func ForgetNetworkBackend() { backendEpoch.Add(1) }

type netBackend struct {
	mu    sync.Mutex
	name  string
	asked time.Time
	epoch int64
	// carry serializes carryOver; said (under mu) keeps what it last logged
	// per network, so one it cannot make again is not logged every pass and
	// its row can say why.
	carry sync.Mutex
	said  map[string]string
	// settled (under carry): SettleHost has reported the host ready.
	settled bool
}

// defaultBackend is what podman uses where it has not said: netavark on
// Linux, CNI elsewhere.
func defaultBackend() string {
	if runtime.GOOS == "linux" {
		return backendNetavark
	}
	return backendCNI
}

// networkBackend is podman's network backend, from its own API. While podman
// does not answer, the last answer stands.
func (b *Backend) networkBackend(ctx context.Context) string {
	b.net.mu.Lock()
	defer b.net.mu.Unlock()
	epoch := backendEpoch.Load()
	if b.net.name != "" && b.net.epoch == epoch && time.Since(b.net.asked) < backendMaxAge {
		return b.net.name
	}
	if name := b.askNetworkBackend(ctx); name != "" {
		b.net.name, b.net.asked, b.net.epoch = name, time.Now(), epoch
		hostnet.SetBackend(name)
		if runtime.GOOS == "freebsd" {
			if name == backendNetavark {
				go b.carryOver()
			} else {
				go b.carryBack()
			}
		}
	}
	if b.net.name == "" {
		return defaultBackend()
	}
	return b.net.name
}

// knownBackend is the last answer without asking, for callers with no
// context to ask in.
func (b *Backend) knownBackend() string {
	b.net.mu.Lock()
	defer b.net.mu.Unlock()
	if b.net.name == "" {
		return defaultBackend()
	}
	return b.net.name
}

func (b *Backend) askNetworkBackend(ctx context.Context) string {
	if b.http == nil {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/info", nil)
	if err != nil {
		return ""
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var info struct {
		Host struct {
			NetworkBackend string `json:"networkBackend"`
		} `json:"host"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&info) != nil {
		return ""
	}
	return info.Host.NetworkBackend
}

// hostInfo is the part of podman's info that says how it does networking.
type hostInfo struct {
	Host struct {
		NetworkBackend     string `json:"networkBackend"`
		NetworkBackendInfo struct {
			DNS struct {
				Path string `json:"path"`
			} `json:"dns"`
		} `json:"networkBackendInfo"`
	} `json:"host"`
}

// NetworkBackendInfo is podman's network backend and the path of the DNS
// server it starts for a network ("" when it has none), for diagnostics
// (pkg/doctor). Asked of the API, and of the CLI when the service is down:
// which backend it is decides what a host check should look for. Both are ""
// when podman cannot say. While podman does not answer (e.g. heavy load), the
// last known answer for the current epoch stands so doctor never flips to the
// wrong backend's checks mid-load.
var netBackendInfoCache struct {
	mu      sync.Mutex
	backend string
	dnsPath string
	asked   time.Time
	epoch   int64
}

func NetworkBackendInfo(ctx context.Context) (backend, dnsPath string) {
	epoch := backendEpoch.Load()
	netBackendInfoCache.mu.Lock()
	if netBackendInfoCache.backend != "" && netBackendInfoCache.epoch == epoch && time.Since(netBackendInfoCache.asked) < backendMaxAge {
		b, d := netBackendInfoCache.backend, netBackendInfoCache.dnsPath
		netBackendInfoCache.mu.Unlock()
		return b, d
	}
	netBackendInfoCache.mu.Unlock()

	var info hostInfo
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/info", nil); err == nil {
		if resp, err := newSocketClient().Do(req); err == nil {
			ok := resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&info) == nil
			resp.Body.Close()
			if ok && info.Host.NetworkBackend != "" {
				netBackendInfoCache.mu.Lock()
				netBackendInfoCache.backend = info.Host.NetworkBackend
				netBackendInfoCache.dnsPath = info.Host.NetworkBackendInfo.DNS.Path
				netBackendInfoCache.asked = time.Now()
				netBackendInfoCache.epoch = epoch
				netBackendInfoCache.mu.Unlock()
				return info.Host.NetworkBackend, info.Host.NetworkBackendInfo.DNS.Path
			}
		}
	}
	out, err := exec.CommandContext(ctx, "podman", "info", "--format", "json").Output()
	if err == nil && json.Unmarshal(out, &info) == nil && info.Host.NetworkBackend != "" {
		netBackendInfoCache.mu.Lock()
		netBackendInfoCache.backend = info.Host.NetworkBackend
		netBackendInfoCache.dnsPath = info.Host.NetworkBackendInfo.DNS.Path
		netBackendInfoCache.asked = time.Now()
		netBackendInfoCache.epoch = epoch
		netBackendInfoCache.mu.Unlock()
		return info.Host.NetworkBackend, info.Host.NetworkBackendInfo.DNS.Path
	}

	netBackendInfoCache.mu.Lock()
	defer netBackendInfoCache.mu.Unlock()
	if netBackendInfoCache.backend != "" && netBackendInfoCache.epoch == epoch {
		return netBackendInfoCache.backend, netBackendInfoCache.dnsPath
	}
	return "", ""
}

// NetworkBackends is podman's network backend as its API service runs with
// and as the CLI would start with now, for diagnostics (pkg/doctor). The
// service reads its configuration once at start, so the two part ways when
// podman or its configuration changes under a running service. Either is ""
// when it cannot be asked.
func NetworkBackends(ctx context.Context) (service, cli string) {
	var info hostInfo
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/info", nil); err == nil {
		if resp, err := newSocketClient().Do(req); err == nil {
			if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&info) == nil {
				service = info.Host.NetworkBackend
			}
			resp.Body.Close()
		}
	}
	out, err := exec.CommandContext(ctx, "podman", "info", "--format", "{{.Host.NetworkBackend}}").Output()
	if err == nil {
		cli = strings.TrimSpace(string(out))
	}
	return service, cli
}

// DNSContainers names the running containers attached to a network podman
// answers names on, for diagnostics (pkg/doctor): the ones a change of its
// DNS server's port has to restart. nil when podman cannot say.
func DNSContainers(ctx context.Context) []string {
	client := newSocketClient()
	get := func(path string, v any) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/"+path, nil)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(v) == nil
	}
	var nets []struct {
		Name string `json:"name"`
		DNS  bool   `json:"dns_enabled"`
	}
	var cts []struct {
		Names    []string `json:"Names"`
		Networks []string `json:"Networks"`
	}
	if !get("networks/json", &nets) || !get("containers/json", &cts) {
		return nil
	}
	return dnsContainers(nets2map(nets), cts2list(cts))
}

type ctNets struct {
	name string
	nets []string
}

func nets2map(nets []struct {
	Name string `json:"name"`
	DNS  bool   `json:"dns_enabled"`
}) map[string]bool {
	m := map[string]bool{}
	for _, n := range nets {
		m[n.Name] = n.DNS
	}
	return m
}

func cts2list(cts []struct {
	Names    []string `json:"Names"`
	Networks []string `json:"Networks"`
}) []ctNets {
	var out []ctNets
	for _, c := range cts {
		if len(c.Names) > 0 {
			out = append(out, ctNets{c.Names[0], c.Networks})
		}
	}
	return out
}

// dnsContainers picks the containers on at least one network with DNS on.
func dnsContainers(dns map[string]bool, cts []ctNets) []string {
	var out []string
	for _, c := range cts {
		for _, n := range c.nets {
			if dns[n] {
				out = append(out, c.name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// reportsAddresses says podman itself knows each container's address and
// MAC. Under CNI on FreeBSD it does not: it cannot parse a third-party
// plugin's conflist, so they are read from CNI's own state (hostnet).
func (b *Backend) reportsAddresses(ctx context.Context) bool {
	return runtime.GOOS == "linux" || b.networkBackend(ctx) == backendNetavark
}

// lanByPlugin says LAN networks here are conflists for the epair plugin:
// podman on FreeBSD with the CNI backend.
func (b *Backend) lanByPlugin() bool {
	return runtime.GOOS == "freebsd" && b.knownBackend() == backendCNI
}

// SettleHost, when set, puts in place what a FreeBSD host needs for podman's
// netavark backend (its pf rules, a free port for its DNS server) and reports
// whether the host is ready. It is called before networks are carried over
// and before any stack starts, again on the next pass while it reports false.
// Set by the daemon: the checks and their fixes live in the doctor package,
// which this package cannot import.
var SettleHost func(ctx context.Context) bool

// CarriedOver, when set, is told each network made again on the other
// backend, and which backend that was ("netavark" or "cni").
var CarriedOver func(name, backend string)

// carryOver makes the networks podman's CNI backend left behind again under
// netavark, so a host that switched backends has nothing to redo by hand:
// same names, bridges, subnets and ranges, and the stacks on them start as
// they are. Not the ones compose makes per project (compose makes those
// again) and not ones made for another engine. A network whose containers
// still run on the old definition is left for the next pass.
func (b *Backend) carryOver() {
	// One pass at a time; a start that arrives during one waits for it, so
	// a stack is not started against a network about to be made again.
	b.net.carry.Lock()
	defer b.net.carry.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// What the host itself needs for netavark comes first: a stack started
	// before it would come up with no way out and no names.
	if SettleHost != nil && !b.net.settled {
		b.net.settled = SettleHost(ctx)
	}
	seen := map[string]bool{}
	defer func() {
		// What was said about a network that is gone is no longer true.
		b.net.mu.Lock()
		for name := range b.net.said {
			if !seen[name] {
				delete(b.net.said, name)
			}
		}
		b.net.mu.Unlock()
	}()
	for _, d := range hostnet.List() {
		if !d.Legacy || d.Compose || d.Name == defaultBridge || (d.For != "" && d.For != "podman") {
			continue
		}
		if d.Type != "bridge" && d.Type != epairPlugin {
			continue
		}
		seen[d.Name] = true
		said := "made again under netavark"
		if _, err := b.RemakeNetwork(ctx, d.Name); err != nil {
			said = "not carried over to netavark: " + err.Error()
		}
		b.net.mu.Lock()
		if b.net.said == nil {
			b.net.said = map[string]string{}
		}
		changed := b.net.said[d.Name] != said
		b.net.said[d.Name] = said
		b.net.mu.Unlock()
		if changed {
			log.Printf("network %s: %s", d.Name, said)
			if CarriedOver != nil && said == "made again under netavark" {
				CarriedOver(d.Name, "netavark")
			}
		}
	}
}

// carryBack is carryOver the other way: after podman went back to CNI
// (netavark-migrate back, by fjord or by hand), the networks made under
// netavark, which the script keeps in its backup, are made again as CNI
// networks with the same names, bridges, subnets and ranges, so the stacks
// on them start as they were.
func (b *Backend) carryBack() {
	b.net.carry.Lock()
	defer b.net.carry.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	seen := map[string]bool{}
	defer func() {
		b.net.mu.Lock()
		for name := range b.net.said {
			if !seen[name] {
				delete(b.net.said, name)
			}
		}
		b.net.mu.Unlock()
	}()
	for _, d := range hostnet.List() {
		if !d.Legacy || d.Compose || d.Name == defaultBridge || (d.For != "" && d.For != "podman") {
			continue
		}
		if d.Type != "bridge" && d.Type != epairPlugin {
			continue
		}
		seen[d.Name] = true
		said := "made again under CNI"
		if _, err := b.remakeUnderCNI(ctx, d); err != nil {
			said = "not carried back to CNI: " + err.Error()
		}
		b.net.mu.Lock()
		if b.net.said == nil {
			b.net.said = map[string]string{}
		}
		changed := b.net.said[d.Name] != said
		b.net.said[d.Name] = said
		b.net.mu.Unlock()
		if changed {
			log.Printf("network %s: %s", d.Name, said)
			if CarriedOver != nil && said == "made again under CNI" {
				CarriedOver(d.Name, "cni")
			}
		}
	}
}

// carryNote is why the last pass could not make a network again, or "".
func (b *Backend) carryNote(name string) string {
	b.net.mu.Lock()
	defer b.net.mu.Unlock()
	for _, prefix := range []string{"not carried over to netavark: ", "not carried back to CNI: "} {
		if why, ok := strings.CutPrefix(b.net.said[name], prefix); ok {
			return why
		}
	}
	return ""
}

// heldOnOldNetwork says why a stack cannot be stopped and started now, ""
// when it can: a network it uses is still the CNI one and containers of
// other stacks run on it. podman under netavark starts nothing on that
// network until it is made again, and it cannot be made again while anything
// runs on the old one, so this stack would stop and not come back. That is
// the state after podman moves to netavark without a reboot.
func (b *Backend) heldOnOldNetwork(ctx context.Context, s *stack.Stack) string {
	if runtime.GOOS != "freebsd" || b.networkBackend(ctx) != backendNetavark {
		return ""
	}
	cs, err := b.listStackContainers(ctx, s.Name)
	if err != nil || len(cs) == 0 {
		return ""
	}
	mine := map[string]bool{}
	var nets []string
	for _, c := range cs {
		for _, n := range c.Names {
			mine[n] = true
		}
		for _, n := range c.Networks {
			if !slices.Contains(nets, n) {
				nets = append(nets, n)
			}
		}
	}
	sort.Strings(nets)
	for _, n := range nets {
		if def, ok := hostnet.Get(n); !ok || !def.Legacy {
			continue
		}
		users, err := b.networkUsersRunning(ctx, n)
		if err != nil {
			continue
		}
		if others := notIn(users, mine); len(others) > 0 {
			return fmt.Sprintf("%s is left as it is: its network %s was made for podman's CNI network backend, and podman here now uses netavark. "+
				"fjord makes the network again once nothing runs on the old one, but %s is still on it, so %s would stop and not start again. "+
				"Stop the stacks that share %s, then start them; or reboot this host, which moves everything at once",
				s.Name, n, strings.Join(others, ", "), s.Name, n)
		}
	}
	return ""
}

// notIn is names without the ones in the set.
func notIn(names []string, set map[string]bool) []string {
	var out []string
	for _, n := range names {
		if !set[n] {
			out = append(out, n)
		}
	}
	return out
}

// dropOtherBackendContainers removes a stack's stopped containers that name
// a network podman does not have, and the definition the CNI backend left
// for the stack's own network. After podman moves to netavark such a
// container can never start ("unable to find network"), and compose keeps
// it instead of making a new one; with it gone, compose up makes the network
// and the container again. Data lives in volumes and is not touched.
func (b *Backend) dropOtherBackendContainers(ctx context.Context, pw io.Writer, s *stack.Stack) {
	if runtime.GOOS != "freebsd" {
		return
	}
	if b.networkBackend(ctx) == backendCNI {
		// Back on CNI: the networks made under netavark come first, or the
		// stack starts before its network exists again.
		b.carryBack()
		return
	}
	// Asked again before every start: what holds port 53 may have arrived
	// since the host was first set up.
	b.net.carry.Lock()
	b.net.settled = false
	b.net.carry.Unlock()
	b.carryOver()
	cs, err := b.listStackContainers(ctx, s.Name)
	if err != nil || len(cs) == 0 {
		return
	}
	out, err := exec.CommandContext(ctx, "podman", "network", "ls", "--format", "{{.Name}}").Output()
	if err != nil {
		return
	}
	known := map[string]bool{}
	for _, n := range strings.Fields(string(out)) {
		known[n] = true
	}
	var stale []string
	for _, c := range cs {
		if c.State == "running" || len(c.Names) == 0 {
			continue
		}
		for _, n := range c.Networks {
			if !known[n] {
				stale = append(stale, c.Names[0])
				break
			}
		}
	}
	if len(stale) == 0 {
		return
	}
	fmt.Fprintf(pw, "[fjord] %s was made before podman moved to the netavark network backend and cannot start as it is; making it again (its data is kept)\n", strings.Join(stale, ", "))
	// podman reports the missing network as an error and removes the
	// container all the same.
	_ = exec.CommandContext(ctx, "podman", append([]string{"rm", "-f"}, stale...)...).Run()
	prefix := strings.ToLower(s.Name) + "_"
	for _, d := range hostnet.List() {
		if d.Legacy && d.Compose && strings.HasPrefix(d.Name, prefix) {
			path := hostnet.ConflistPath(d.Name)
			if old, err := os.ReadFile(path); err == nil {
				keepConflist(d.Name, old)
			}
			_ = os.Remove(path)
			retireCNIBridge(ctx, d.Bridge)
			retireCNIDNS(ctx, d.Name)
		}
	}
}
