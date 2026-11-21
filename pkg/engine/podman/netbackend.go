package podman

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
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
