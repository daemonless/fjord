package podman

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/hostnet"
)

// backendServer is a libpod API that reports one network backend, or fails
// while *name is empty.
func backendServer(t *testing.T, name *string) *Backend {
	t.Helper()
	t.Cleanup(hostnet.UseConflists(t.TempDir()))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v4.0.0/libpod/info" || *name == "" {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"host":{"networkBackend":"` + *name + `"}}`))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return &Backend{http: &http.Client{Timeout: time.Second, Transport: &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) { return u, nil },
	}}}
}

func TestNetworkBackend(t *testing.T) {
	ctx := context.Background()
	name := ""
	b := backendServer(t, &name)
	if got := b.networkBackend(ctx); got != defaultBackend() {
		t.Errorf("podman silent: %q, want the default %q", got, defaultBackend())
	}

	name = backendNetavark
	if got := b.networkBackend(ctx); got != backendNetavark {
		t.Errorf("got %q", got)
	}
	if !b.reportsAddresses(ctx) {
		t.Error("netavark reports addresses")
	}
	if b.lanByPlugin() {
		t.Error("no epair conflists under netavark")
	}
	if got := b.NetworkKinds(); len(got) == 0 || got[len(got)-1].ID != "nat" {
		t.Errorf("kinds: %+v", got)
	}
	if runtime.GOOS == "freebsd" {
		k := b.NetworkKinds()
		if len(k) != 2 || k[0].ID != "lan" || k[0].SupportsDHCP != netavarkPlugin() || b.networkNote() != "" {
			t.Errorf("FreeBSD netavark offers %+v, note %q", k, b.networkNote())
		}
	}

	// The answer is kept while it is fresh, and stands while podman is down.
	name = backendCNI
	if got := b.networkBackend(ctx); got != backendNetavark {
		t.Errorf("asked again inside the max age: %q", got)
	}
	b.net.asked = time.Now().Add(-2 * backendMaxAge)
	name = ""
	if got := b.networkBackend(ctx); got != backendNetavark {
		t.Errorf("podman down: %q, want the last answer", got)
	}
	name = backendCNI
	if got := b.networkBackend(ctx); got != backendCNI {
		t.Errorf("after the max age: %q", got)
	}
	if runtime.GOOS != "linux" && b.reportsAddresses(ctx) {
		t.Error("CNI off Linux does not report addresses")
	}
}

// Only containers on a network podman answers names on are restarted when
// its DNS server moves: not the default network's, not a --disable-dns one's.
func TestDNSContainers(t *testing.T) {
	dns := map[string]bool{"app_default": true, "priv": true, "podman": false, "quiet": false}
	got := dnsContainers(dns, []ctNets{
		{"web", []string{"app_default"}},
		{"db", []string{"quiet", "priv"}},
		{"lone", []string{"podman"}},
		{"mute", []string{"quiet"}},
		{"host", nil},
	})
	if len(got) != 2 || got[0] != "db" || got[1] != "web" {
		t.Errorf("got %v, want [db web]", got)
	}
}
