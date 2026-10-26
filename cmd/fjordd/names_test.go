package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/stack"
)

func TestNameLabel(t *testing.T) {
	for in, want := range map[string]string{"radarr": "radarr", "Radarr 4K": "radarr-4k", "my_app.v2": "my-app-v2", "-x-": "x"} {
		if got := nameLabel(in); got != want {
			t.Errorf("nameLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFillVar(t *testing.T) {
	env := map[string]string{"RP_PAIRING_KEY": "k"}
	if fillVar("${RP_PAIRING_KEY}", env) != "k" || fillVar("${MISSING}", env) != "" || fillVar("literal", env) != "literal" {
		t.Error("fillVar")
	}
}

func TestManifestNamesProvider(t *testing.T) {
	m, err := manifest.Parse("services:\n  proxy:\n    image: x\nx-fjord:\n  provides:\n    names:\n      port: 8443\n      key: ${RP_PAIRING_KEY}\n      setup_token: ${RP_SETUP_TOKEN}\n")
	if err != nil {
		t.Fatal(err)
	}
	rp := m.Names
	if rp == nil || rp.Port != 8443 || rp.Key != "${RP_PAIRING_KEY}" || rp.SetupToken != "${RP_SETUP_TOKEN}" {
		t.Fatalf("provides.names: %+v", rp)
	}
}

// fakeProxy is a proxy's API as fjord sees it: status, trust, root, hosts.
type fakeProxy struct {
	srv      *httptest.Server
	complete bool
	key      string
	// proxyName is its own name; "" means example.com.
	proxyName string
	hosts     int
	list      []providerHost
	updated   []providerHost
}

// selfSigned is a fresh certificate for example.com (or the names given)
// and 127.0.0.1: httptest gives every server the same built-in one, which
// cannot tell two apart.
func selfSigned(t *testing.T, names ...string) tls.Certificate {
	if len(names) == 0 {
		names = []string{"example.com"}
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "other proxy root"},
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    names, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
}

func newFakeProxy(t *testing.T, key string, certs ...tls.Certificate) *fakeProxy {
	f := &fakeProxy{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.key {
			w.WriteHeader(401)
			return
		}
		out := map[string]any{"setup": map[string]any{"complete": f.complete}}
		if f.complete {
			name := f.proxyName
			if name == "" {
				name = "example.com"
			}
			out["domain"], out["proxyName"], out["trustUrl"] = "apps.test", name, "http://"+name+"/trust"
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/v1/trust", func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256(f.srv.Certificate().Raw)
		json.NewEncoder(w).Encode(map[string]any{"needed": true, "rootUrl": "/trust/root.crt", "fingerprint": strings.ToUpper(hex.EncodeToString(sum[:]))})
	})
	mux.HandleFunc("/trust/root.crt", func(w http.ResponseWriter, r *http.Request) {
		pem.Encode(w, &pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw})
	})
	mux.HandleFunc("/api/v1/hosts", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.key {
			w.WriteHeader(401)
			return
		}
		f.hosts++
		json.NewEncoder(w).Encode(map[string]any{"hosts": f.list})
	})
	// PUT: the whole host back, as the real API wants it.
	mux.HandleFunc("/api/v1/hosts/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			w.WriteHeader(405)
			return
		}
		// As strict as the real provider: only the input fields.
		var in struct {
			Name            string   `json:"name"`
			ExtraNames      []string `json:"extraNames"`
			Target          any      `json:"target"`
			Access          string   `json:"access"`
			TrustedNetworks []string `json:"trustedNetworks"`
			Ref             string   `json:"ref"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"error": "request body: " + err.Error()})
			return
		}
		b, _ := json.Marshal(in)
		var h providerHost
		json.Unmarshal(b, &h)
		h.ID = strings.TrimPrefix(r.URL.Path, "/api/v1/hosts/")
		f.updated = append(f.updated, h)
		json.NewEncoder(w).Encode(h)
	})
	f.srv = httptest.NewUnstartedServer(mux)
	if len(certs) > 0 {
		f.srv.TLS = &tls.Config{Certificates: certs}
	}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

// Before setup the proxy's certificate is a throwaway and fjord talks to it
// unverified; once setup is done fjord pins the root the proxy serves and
// verifies every later connection against it. A different certificate is
// then refused, unless the proxy says it is back at first run, which
// drops the pin.
func TestPinFlow(t *testing.T) {
	f := newFakeProxy(t, "k")
	p := &provider{Stack: "rp", base: f.srv.URL, key: "k", pinPath: filepath.Join(t.TempDir(), "secrets", "names-rp.json")}
	ctx := context.Background()

	st, _, err := p.status(ctx)
	if err != nil || st.Setup.Complete || p.pin != nil {
		t.Fatalf("before setup: %v %+v pin=%v", err, st, p.pin)
	}

	f.complete = true
	st, _, err = p.status(ctx)
	if err != nil || !st.Setup.Complete || p.pin == nil || p.pin.TrustName != "example.com" || p.pin.Root == "" {
		t.Fatalf("after setup: %v pin=%+v", err, p.pin)
	}
	if loadPin(p.pinPath) == nil {
		t.Fatal("pin not saved")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(p.pin.Root)) {
		t.Fatal("pinned root is not a certificate")
	}
	// Verified calls work (the test server's certificate is for example.com).
	if _, err := p.hosts(ctx); err != nil {
		t.Fatalf("verified call: %v", err)
	}

	// Another server, another certificate: refused.
	other := newFakeProxy(t, "k", selfSigned(t))
	other.complete = true
	q := &provider{Stack: "rp", base: other.srv.URL, key: "k", pinPath: p.pinPath, pin: loadPin(p.pinPath)}
	if _, _, err := q.status(ctx); err == nil || !strings.Contains(err.Error(), "not the one fjord trusts") {
		t.Fatalf("other certificate: %v", err)
	}
	if other.hosts != 0 || loadPin(p.pinPath) == nil {
		t.Fatal("a refused proxy was called, or the pin went")
	}

	// A new ending set in its Settings: a new own name and a new root. It
	// is pinned again under the new name and verified calls work.
	moved := newFakeProxy(t, "k", selfSigned(t, "proxy.new.test"))
	moved.complete, moved.proxyName = true, "proxy.new.test"
	m := &provider{Stack: "rp", base: moved.srv.URL, key: "k", pinPath: p.pinPath, pin: loadPin(p.pinPath)}
	if st, _, err := m.status(ctx); err != nil || st.ProxyName != "proxy.new.test" || m.pin == nil || m.pin.TrustName != "proxy.new.test" {
		t.Fatalf("new ending: %v pin=%+v", err, m.pin)
	}
	if _, err := m.hosts(ctx); err != nil {
		t.Fatalf("verified call after the new ending: %v", err)
	}

	// The same other server at first run (reset): the pin goes, no error.
	other.complete = false
	if st, _, err := q.status(ctx); err != nil || st.Setup.Complete || q.pin != nil || loadPin(p.pinPath) != nil {
		t.Fatalf("reset proxy: %v pin=%v", err, q.pin)
	}
}

// A refused key is reported as such, not as an unreachable proxy.
func TestProxyKeyRefused(t *testing.T) {
	f := newFakeProxy(t, "right")
	p := &provider{Stack: "rp", base: f.srv.URL, key: "wrong", pinPath: filepath.Join(t.TempDir(), "p.json")}
	if _, code, err := p.status(context.Background()); code != 401 || err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("refused key: %d %v", code, err)
	}
}
func TestValidNameKey(t *testing.T) {
	for key, ok := range map[string]bool{"radarr": true, "crafting-apps/photocraft": true, "a/b/c": false, "../x": false, "x/": false, "": false} {
		if validNameKey(key) != ok {
			t.Errorf("validNameKey(%q) = %v, want %v", key, !ok, ok)
		}
	}
}

// A name points where a browser opens the app. When that moves, the name
// follows: an app on its own address that got a new lease, or one published
// on this host whose port changed on Apply.
func TestCurrentTarget(t *testing.T) {
	lan := func(n string) bool { return n == "lan" }
	own := []serviceView{{Name: "app", Networks: attachments("lan"), Live: map[string]string{"lan": "192.168.4.50"}}}
	if a, p := currentTarget(&stack.Stack{}, own, "", 80, lan); a != "192.168.4.50" || p != 80 {
		t.Fatalf("own address: %s:%d", a, p)
	}
	many := []serviceView{
		{Name: "photocraft", Networks: attachments("lan"), Live: map[string]string{"lan": "192.168.4.51"}},
		{Name: "vectorcraft", Networks: attachments("lan"), Live: map[string]string{"lan": "192.168.4.52"}},
	}
	if a, p := currentTarget(&stack.Stack{}, many, "vectorcraft", 80, lan); a != "192.168.4.52" || p != 80 {
		t.Fatalf("one app of several: %s:%d", a, p)
	}
	if a, p := currentTarget(&stack.Stack{}, many, "gone", 80, lan); a != "" || p != 0 {
		t.Fatalf("missing app: %s:%d", a, p)
	}
	published := &stack.Stack{Compose: "services:\n  app:\n    ports:\n      - \"${WEB_PORT}:7878\"\nx-fjord:\n  web_port: \"${WEB_PORT}\"\n", Env: "WEB_PORT=9090\n"}
	bridge := []serviceView{{Name: "app", Networks: attachments("podman"), Live: map[string]string{"podman": "10.88.0.5"}}}
	if a, p := currentTarget(published, bridge, "", 7878, lan); a != "" || p != 9090 {
		t.Fatalf("published: %q:%d", a, p)
	}
	if a, p := currentTarget(&stack.Stack{Compose: "services: {}\n"}, bridge, "", 7878, lan); a != "" || p != 0 {
		t.Fatalf("no page: %q:%d", a, p)
	}
}

func attachments(network string) []composepkg.Attachment {
	return []composepkg.Attachment{{Network: network}}
}

// The status loop found an app's published port changed: the provider's
// host is sent back whole with the new target, names and access intact.
func TestSyncTargetsUpdatesTheHost(t *testing.T) {
	f := newFakeProxy(t, "k")
	f.complete = true
	var h providerHost
	h.ID, h.Name, h.ExtraNames, h.Access, h.Ref = "h1", "radarr", []string{"movies"}, "home", nameRef("radarr")
	h.Target.Address, h.Target.Port, h.Target.HTTPS = "192.168.4.103", 7878, false
	f.list = []providerHost{h}
	p := &provider{Stack: "rp", base: f.srv.URL, key: "k", pinPath: filepath.Join(t.TempDir(), "names-rp.json")}
	st := &stack.Stack{Name: "radarr", Compose: "services:\n  radarr:\n    ports:\n      - \"${WEB_PORT}:7878\"\nx-fjord:\n  web_port: \"${WEB_PORT}\"\n", Env: "WEB_PORT=7879\n"}
	var status engine.StackStatus
	if err := p.syncStack(context.Background(), st, status); err != nil {
		t.Fatal(err)
	}
	if len(f.updated) != 1 || f.updated[0].Target.Port != 7879 || f.updated[0].Target.Address != "192.168.4.103" ||
		f.updated[0].Name != "radarr" || len(f.updated[0].ExtraNames) != 1 || f.updated[0].Access != "home" {
		t.Fatalf("updated: %+v", f.updated)
	}
	// Nothing moved: nothing sent.
	f.list[0].Target.Port, f.updated = 7879, nil
	if err := p.syncStack(context.Background(), st, status); err != nil || len(f.updated) != 0 {
		t.Fatalf("unchanged: %v %+v", err, f.updated)
	}
}

// A new label goes to the provider as the whole host with only the name
// changed; an app without a name, or a label that is nothing once cleaned
// up, is refused here without a call.
func TestRenameKeepsTheHost(t *testing.T) {
	f := newFakeProxy(t, "k")
	f.complete = true
	var h providerHost
	h.ID, h.Name, h.ExtraNames, h.Access, h.Ref = "h1", "radarr", []string{"movies"}, "home", nameRef("radarr")
	h.Target.Address, h.Target.Port = "192.168.4.103", 7878
	f.list = []providerHost{h}
	p := &provider{Stack: "rp", base: f.srv.URL, key: "k", pinPath: filepath.Join(t.TempDir(), "names-rp.json")}
	got, err := p.rename(context.Background(), "radarr", "Movies 4K")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "movies-4k" || len(f.updated) != 1 || f.updated[0].ID != "h1" || f.updated[0].Name != "movies-4k" ||
		f.updated[0].Target.Port != 7878 || len(f.updated[0].ExtraNames) != 1 || f.updated[0].Ref != nameRef("radarr") {
		t.Fatalf("updated: %+v", f.updated)
	}
	if _, err := p.rename(context.Background(), "sonarr", "tv"); !errors.Is(err, errNoName) {
		t.Fatalf("no name: %v", err)
	}
}

// Other fjords' names and hand-made ones are taken; this fjord's own are
// not (they are its names, by stack).
func TestTakenNamesLeavesOutOurOwn(t *testing.T) {
	mk := func(name, ref string, extra ...string) providerHost {
		var h providerHost
		h.Name, h.Ref, h.ExtraNames = name, ref, extra
		return h
	}
	hosts := []providerHost{
		mk("radarr", nameRef("radarr")),
		mk("sonarr", "fjord:other-id:sonarr", "tv"),
		mk("router", "", "gw"),
		mk("tv", "manual"),
	}
	got := takenNames(hosts)
	want := []string{"gw", "router", "sonarr", "tv"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("taken: %v, want %v", got, want)
	}
}

// A host on two networks, opened from the one the provider is not on: the
// other address is tried. An app on an address of its own gets no
// guesses, since it is not on this host.
func TestOtherHostAddresses(t *testing.T) {
	own := []string{"192.168.86.194", "192.168.4.180", "10.88.0.1"}
	if got := strings.Join(otherHostAddresses("192.168.86.194", own), ","); got != "192.168.4.180,10.88.0.1" {
		t.Fatalf("other: %s", got)
	}
	if got := otherHostAddresses("192.168.4.50", own); got != nil {
		t.Fatalf("an app's own address: %v", got)
	}
}

// The browser's address is what the provider is asked to reach; from the
// host itself that is 127.0.0.1, which nothing elsewhere reaches, so the
// host's own addresses stand in (a Linux desktop opening localhost:3567
// got "proxy cannot reach 127.0.0.1:8000", netlab-linux 2026-10-26).
func TestAddressCandidatesLoopbackMeansTheHost(t *testing.T) {
	own := []string{"192.168.86.194", "192.168.4.180"}
	if got := addressCandidates("127.0.0.1", own); !reflect.DeepEqual(got, own) {
		t.Fatalf("loopback: %v", got)
	}
	if got := addressCandidates("192.168.4.180", own); !reflect.DeepEqual(got, []string{"192.168.4.180", "192.168.86.194"}) {
		t.Fatalf("own address first, the other after: %v", got)
	}
	if got := addressCandidates("192.168.4.50", own); !reflect.DeepEqual(got, []string{"192.168.4.50"}) {
		t.Fatalf("an app's own address is tried alone: %v", got)
	}
	if got := addressCandidates("127.0.0.1", nil); !reflect.DeepEqual(got, []string{"127.0.0.1"}) {
		t.Fatalf("no own addresses: %v", got)
	}
}
