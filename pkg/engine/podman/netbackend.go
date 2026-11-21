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
	"sort"
	"strings"
	"sync"
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

type netBackend struct {
	mu    sync.Mutex
	name  string
	asked time.Time
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
	if b.net.name != "" && time.Since(b.net.asked) < backendMaxAge {
		return b.net.name
	}
	if name := b.askNetworkBackend(ctx); name != "" {
		b.net.name, b.net.asked = name, time.Now()
		hostnet.SetBackend(name)
		if name == backendNetavark && runtime.GOOS == "freebsd" {
			go b.carryOver()
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
// when podman cannot say.
func NetworkBackendInfo(ctx context.Context) (backend, dnsPath string) {
	var info hostInfo
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/info", nil); err == nil {
		if resp, err := newSocketClient().Do(req); err == nil {
			ok := resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&info) == nil
			resp.Body.Close()
			if ok && info.Host.NetworkBackend != "" {
				return info.Host.NetworkBackend, info.Host.NetworkBackendInfo.DNS.Path
			}
		}
	}
	out, err := exec.CommandContext(ctx, "podman", "info", "--format", "json").Output()
	if err != nil || json.Unmarshal(out, &info) != nil {
		return "", ""
	}
	return info.Host.NetworkBackend, info.Host.NetworkBackendInfo.DNS.Path
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

// CarriedOver, when set, is told each network carryOver made again.
var CarriedOver func(name string)

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
				CarriedOver(d.Name)
			}
		}
	}
}

// carryNote is why the last pass could not make a network again, or "".
func (b *Backend) carryNote(name string) string {
	b.net.mu.Lock()
	defer b.net.mu.Unlock()
	if why, ok := strings.CutPrefix(b.net.said[name], "not carried over to netavark: "); ok {
		return why
	}
	return ""
}

// dropOtherBackendContainers removes a stack's stopped containers that name
// a network podman does not have, and the definition the CNI backend left
// for the stack's own network. After podman moves to netavark such a
// container can never start ("unable to find network"), and compose keeps
// it instead of making a new one; with it gone, compose up makes the network
// and the container again. Data lives in volumes and is not touched.
func (b *Backend) dropOtherBackendContainers(ctx context.Context, pw io.Writer, s *stack.Stack) {
	if runtime.GOOS != "freebsd" || b.networkBackend(ctx) != backendNetavark {
		return
	}
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
			destroyIdleCNIBridge(ctx, d.Bridge)
		}
	}
}
