package podman

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/hostnet"
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
