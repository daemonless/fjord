package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/stack"

	"gopkg.in/yaml.v3"
)

// A names provider is an app installed from a catalog that gives the other
// apps names (radarr.apps.lan): a reverse proxy, or anything else that
// serves the names API (docs/names-provider.md). Its manifest says how
// fjord reaches it (x-fjord.provides.names): the container port of its
// HTTPS API and the variable holding the key fjord made at install. fjord
// talks to it the way any script would, over HTTPS with that key as a
// bearer token; the provider's own root certificate is pinned once its
// setup is done (see providerPin). Nothing here knows which app it is.

// provider is a paired names provider: where its API is and how to sign the calls.
type provider struct {
	Stack      string // the stack that provides names, or a label for a linked provider
	base       string // https://<address>:<port>
	key        string
	setupToken string
	pinPath    string
	pin        *providerPin // nil until the provider is set up and its root is known
	linked     bool         // linked by hand (Settings, Names), not a stack here
	// publicRoots verify a provider with public certificates: nil is the
	// system's. Tests set their own.
	publicRoots *x509.CertPool
}

// providerPin is what fjord trusts about a provider, kept in fjord's
// root-only secrets folder. Until the provider's setup is done it serves a
// throwaway certificate, so the first calls cannot be verified; once it
// reports its ending, fjord takes its root certificate (the one every
// device is asked to trust) and checks every later connection against it.
// A provider with public certificates has no root of its own: Root is empty
// and the system roots apply. TrustName is what the certificate is for
// (proxy.<ending>), sent as the server name since fjord dials by address.
type providerPin struct {
	TrustName string `json:"trustName"`
	Root      string `json:"root,omitempty"` // PEM
}

// providerHost is a host as the provider's API returns it: what fjord reads,
// and what it must send back whole when it updates one.
type providerHost struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	ExtraNames      []string `json:"extraNames,omitempty"`
	URL             string   `json:"url"`
	Ref             string   `json:"ref"`
	Access          string   `json:"access"`
	TrustedNetworks []string `json:"trustedNetworks,omitempty"`
	Target          struct {
		Address         string `json:"address"`
		Port            int    `json:"port"`
		HTTPS           bool   `json:"https"`
		AcceptUntrusted bool   `json:"acceptUntrusted"`
	} `json:"target"`
	// OwnOrigin: the app wants requests from its own address (the stack's
	// x-fjord own_origin). Sent only when true, so a provider that predates
	// it is never handed a field it refuses.
	OwnOrigin bool `json:"ownOrigin,omitempty"`
}

// input is the host as the provider's PUT takes it: the editable fields
// only. The record it hands out carries more (id, url, names), and the
// provider refuses unknown fields; sending the record back broke rename
// and the port sync against the real thing while a lax fake passed.
func (h providerHost) input() map[string]any {
	extra := h.ExtraNames
	if extra == nil {
		extra = []string{}
	}
	nets := h.TrustedNetworks
	if nets == nil {
		nets = []string{}
	}
	in := map[string]any{
		"name": h.Name, "extraNames": extra, "target": h.Target, "access": h.Access,
		"trustedNetworks": nets, "ref": h.Ref,
	}
	if h.OwnOrigin {
		in["ownOrigin"] = true // absent keeps the provider's value
	}
	return in
}

// namesManifests caches which apps give names, by app id, for a minute:
// a catalog refresh (manual or scheduled) may change a manifest, and a
// cache that never expired kept the old answer until the next restart.
var namesManifests struct {
	sync.Mutex
	byApp map[string]namesManifest
}

type namesManifest struct {
	rp *manifest.NamesProvider
	at time.Time
}

// namesOf is the stack's x-fjord.provides.names, nil when its app declares
// none.
func (s *server) namesOf(ctx context.Context, st *stack.Stack) *manifest.NamesProvider {
	if st.State == nil || st.State.Origin.Type != "catalog" || st.State.Origin.AppID == "" || s.cat == nil {
		return nil
	}
	id := st.State.Origin.AppID
	namesManifests.Lock()
	c, ok := namesManifests.byApp[id]
	namesManifests.Unlock()
	if ok && time.Since(c.at) < time.Minute {
		return c.rp
	}
	raw, err := s.cat.ManifestAny(ctx, id)
	if err != nil {
		return c.rp // not cached: the catalog may be back next time
	}
	m, err := manifest.Parse(string(raw))
	var rp *manifest.NamesProvider
	if err == nil {
		rp = m.Names
	}
	namesManifests.Lock()
	if namesManifests.byApp == nil {
		namesManifests.byApp = map[string]namesManifest{}
	}
	namesManifests.byApp[id] = namesManifest{rp: rp, at: time.Now()}
	namesManifests.Unlock()
	return rp
}

// findProvider is the installed names provider, or nil when there is none.
func (s *server) findProvider(ctx context.Context) (*provider, error) {
	stacks, err := s.manager.List()
	if err != nil {
		return nil, err
	}
	for _, st := range stacks {
		rp := s.namesOf(ctx, st)
		if rp == nil {
			continue
		}
		// List reads no .env; the key is in it.
		full, err := s.manager.Get(st.Name)
		if err != nil {
			return nil, err
		}
		env := full.EnvMap()
		key := fillVar(rp.Key, env)
		if key == "" {
			return nil, fmt.Errorf("%s has no pairing key in its settings; reinstall it from the store", st.Name)
		}
		p := &provider{Stack: st.Name, key: key, setupToken: fillVar(rp.SetupToken, env),
			pinPath: filepath.Join(s.fjordRoot, "secrets", "names-"+st.Name+".json")}
		p.pin = loadPin(p.pinPath)
		// full: List carries no compose, and the services come from it. No
		// address yet (just installed, still starting) is the provider with
		// an error, not no provider: the UI keeps looking until it answers.
		base, err := s.providerBase(ctx, full, rp.Port)
		if err != nil {
			return p, err
		}
		p.base = base
		return p, nil
	}
	// No stack of this fjord gives names: a provider linked by hand, if any.
	return s.linkedProvider(), nil
}

// varName is VAR for "${VAR}", "" for a literal.
func varName(v string) string {
	if name, ok := strings.CutPrefix(v, "${"); ok && strings.HasSuffix(name, "}") {
		return strings.TrimSuffix(name, "}")
	}
	return ""
}

// fillVar fills a "${VAR}" from env; a literal comes back as it is.
func fillVar(v string, env map[string]string) string {
	if name, ok := strings.CutPrefix(v, "${"); ok && strings.HasSuffix(name, "}") {
		return env[strings.TrimSuffix(name, "}")]
	}
	return v
}

// providerBase is where the provider's API is reached from here: the
// address a browser would open the stack at (the same rule as Open) and
// the port itself when the container has an address of its own; else the
// container's bridge address, reachable from the host the daemon runs on.
func (s *server) providerBase(ctx context.Context, st *stack.Stack, port int) (string, error) {
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("%s: provides.names.port %d is not a port", st.Name, port)
	}
	status, err := s.backendFor(st).Status(ctx, st)
	if err != nil {
		return "", err
	}
	svcs := stackServices(st, status)
	published := port
	host := linkHost(svcs, ownAddress)
	if host != "" {
		for _, v := range svcs {
			for _, p := range v.Ports {
				if p.ContainerPort == port && p.HostPort > 0 {
					published = p.HostPort
				}
			}
		}
	} else {
		// Published on this host: a browser goes to the host's address,
		// which the daemon does not know; but the daemon IS on the host,
		// where the container's own bridge address answers directly.
		for _, v := range svcs {
			if v.Address == "" {
				continue
			}
			if host == "" {
				host = v.Address
			}
			for _, p := range v.Ports {
				if p.ContainerPort == port {
					host = v.Address
				}
			}
		}
	}
	if host == "" {
		return "", fmt.Errorf("%s is not running yet", st.Name)
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(published)), nil
}

func loadPin(path string) *providerPin {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var p providerPin
	if json.Unmarshal(b, &p) != nil || p.TrustName == "" {
		return nil
	}
	return &p
}

func (p *provider) savePin(pin *providerPin) error {
	if err := os.MkdirAll(filepath.Dir(p.pinPath), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(pin)
	if err := os.WriteFile(p.pinPath, b, 0o600); err != nil {
		return err
	}
	p.pin = pin
	return nil
}

// forgetPin drops what fjord trusted: the provider was set up again.
func (p *provider) forgetPin() {
	os.Remove(p.pinPath)
	p.pin = nil
}

// client is how fjord connects: verified against the pinned root (or the
// system roots for a provider with public certificates), or, before it
// is set up and has a root, not verified at all.
func (p *provider) client() *http.Client {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case p.pin == nil:
		cfg.InsecureSkipVerify = true
	default:
		cfg.ServerName = p.pin.TrustName
		if p.pin.Root != "" {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM([]byte(p.pin.Root))
			cfg.RootCAs = pool
		} else {
			cfg.RootCAs = p.publicRoots
		}
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg}}
}

// errUntrusted: the provider's certificate is not the one fjord pinned.
var errUntrusted = errors.New("its certificate is not the one fjord trusts")

// providerStatus is what GET /api/v1/status says to fjord's key.
type providerStatus struct {
	Domain    string `json:"domain"`
	ProxyName string `json:"proxyName"`
	TrustURL  string `json:"trustUrl"`
	Setup     struct {
		Complete bool `json:"complete"`
	} `json:"setup"`
}

// status reads the provider's status and keeps the pin right: one that
// just finished setup gets pinned; one that no longer matches its pin is
// looked at without it: reset (setup to do again), the pin goes so the
// next setup pins afresh; given a new ending in its Settings (a new own
// name and a new root), it is pinned again under the new name. Anything
// else that does not match stays refused.
func (p *provider) status(ctx context.Context) (providerStatus, int, error) {
	var st providerStatus
	code, err := p.call(ctx, "GET", "/api/v1/status", nil, &st)
	if errors.Is(err, errUntrusted) {
		was := p.pin
		p.pin = nil
		var again providerStatus
		code2, err2 := p.call(ctx, "GET", "/api/v1/status", nil, &again)
		switch {
		case err2 == nil && !again.Setup.Complete:
			p.forgetPin()
			return again, http.StatusOK, nil
		case err2 == nil && again.Setup.Complete && again.ProxyName != "" && again.ProxyName != was.TrustName:
			p.forgetPin()
			if err := p.pinRoot(ctx, again.ProxyName); err != nil {
				return again, code2, fmt.Errorf("cannot take its root certificate: %v", err)
			}
			return again, code2, nil
		case err2 == nil && again.Setup.Complete && again.ProxyName == was.TrustName && was.Root != "":
			// Its own root when setup finished, public certificates now
			// (they arrive after: a free name, a DNS token). Trusted only
			// if it verifies against the public roots, which nobody can
			// fake for this name (netlab, 2026-10-27).
			p.pin = &providerPin{TrustName: was.TrustName}
			var pub providerStatus
			if code3, err3 := p.call(ctx, "GET", "/api/v1/status", nil, &pub); err3 == nil {
				if err := p.savePin(p.pin); err != nil {
					return pub, code3, err
				}
				return pub, code3, nil
			}
		}
		p.pin = was
		return st, code, err
	}
	if err != nil {
		return st, code, err
	}
	if p.pin == nil && st.Setup.Complete && st.Domain != "" && st.ProxyName != "" {
		if err := p.pinRoot(ctx, st.ProxyName); err != nil {
			return st, code, fmt.Errorf("cannot take its root certificate: %v", err)
		}
	}
	return st, code, nil
}

// pinRoot fetches the provider's root (what its trust page serves) and
// pins it. The root's fingerprint must match what it says it is: a stray
// answer on the way does not get pinned.
func (p *provider) pinRoot(ctx context.Context, trustName string) error {
	var trust struct {
		Needed      bool   `json:"needed"`
		Fingerprint string `json:"fingerprint"`
		RootURL     string `json:"rootUrl"`
	}
	if _, err := p.call(ctx, "GET", "/api/v1/trust", nil, &trust); err != nil {
		return err
	}
	if !trust.Needed {
		return p.savePin(&providerPin{TrustName: trustName})
	}
	if trust.RootURL == "" || trust.Fingerprint == "" {
		return errors.New("it has no root certificate yet")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", p.base+trust.RootURL, nil)
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New("the root is not a PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(cert.Raw)
	if !strings.EqualFold(strings.ReplaceAll(trust.Fingerprint, ":", ""), hex.EncodeToString(sum[:])) {
		return errors.New("the root's fingerprint does not match what it reports")
	}
	return p.savePin(&providerPin{TrustName: trustName, Root: string(pem.EncodeToMemory(block))})
}

// call is one request to the provider's API. out may be nil.
func (p *provider) call(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client().Do(req)
	if err != nil {
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			return 0, errUntrusted
		}
		return 0, fmt.Errorf("%s is not answering at %s", p.Stack, p.base)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return resp.StatusCode, fmt.Errorf("%s no longer accepts fjord's key (removed under Connected apps?)", p.Stack)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil && resp.StatusCode/100 == 2 {
			return resp.StatusCode, err
		}
	}
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusConflict {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return resp.StatusCode, errors.New(e.Error)
	}
	return resp.StatusCode, nil
}

func (p *provider) hosts(ctx context.Context) ([]providerHost, error) {
	var out struct {
		Hosts []providerHost `json:"hosts"`
	}
	_, err := p.call(ctx, "GET", "/api/v1/hosts", nil, &out)
	return out.Hosts, err
}

// validNameKey is a stack, or "<stack>/<service>" for one app of a stack of
// several (crafting-apps/photocraft).
func validNameKey(key string) bool {
	st, svc, many := strings.Cut(key, "/")
	return stack.ValidName(st) && (!many || stack.ValidName(svc))
}

// nameRef ties a host to the app it names, so fjord finds it again.
func nameRef(stackName string) string {
	return "fjord:" + instanceID + ":" + stackName
}

// ownKey is the stack (or stack/app) behind a ref this fjord made, by its
// id, or by its host name as refs were written before the id existed.
func ownKey(ref string) (string, bool) {
	if key, ok := strings.CutPrefix(ref, nameRef("")); ok {
		return key, true
	}
	host, _ := os.Hostname()
	return strings.CutPrefix(ref, "fjord:"+strings.SplitN(host, ".", 2)[0]+":")
}

// namesView is GET /api/names.
type namesView struct {
	// Provider is the stack that gives names; "" when none is installed.
	Provider string `json:"provider,omitempty"`
	// SetupToken opens the provider's own setup, until it is done.
	SetupToken string `json:"setupToken,omitempty"`
	Ready      bool   `json:"ready"`
	Domain     string `json:"domain,omitempty"`
	// TrustName is the provider's own name (proxy.<ending>): a browser
	// that loads it over HTTPS trusts the provider.
	TrustName string `json:"trustName,omitempty"`
	TrustURL  string `json:"trustUrl,omitempty"`
	// Disconnected: the provider refuses fjord's key (turned off in its
	// Settings). Its names still work; fjord cannot change them.
	Disconnected bool `json:"disconnected,omitempty"`
	// Names are the hosts fjord made, by stack.
	Names map[string]providerHost `json:"names,omitempty"`
	// Taken are the labels every other host on the provider holds (another
	// fjord's, or made by hand), so a suggested name is free before it is
	// offered, not refused after.
	Taken []string `json:"taken,omitempty"`
	Error string   `json:"error,omitempty"`
	// Linked: the provider was linked by hand (Settings, Names), not
	// installed here. Self is this fjord, as providers see it.
	Linked bool      `json:"linked,omitempty"`
	Self   *Instance `json:"self,omitempty"`
}

func (s *server) namesState(ctx context.Context) namesView {
	self := s.instance()
	p, err := s.findProvider(ctx)
	if p == nil || err != nil {
		v := namesView{Self: &self}
		if p != nil {
			v.Provider = p.Stack
			v.Linked = p.linked
			// The token is in the stack's .env from the moment it is
			// saved: Open carries it while the provider is still coming
			// up, so the first click lands on its setup verified, not on
			// a page asking for a token from the log (jupiter, 2026-10-26).
			v.SetupToken = p.setupToken
		}
		if err != nil {
			v.Error = err.Error()
		}
		return v
	}
	v := namesView{Provider: p.Stack, Linked: p.linked, Self: &self}
	st, code, err := p.status(ctx)
	if err != nil {
		v.Disconnected = code == http.StatusUnauthorized
		v.Error = err.Error()
		if !v.Disconnected {
			v.SetupToken = p.setupToken // not answering yet: see above
		}
		return v
	}
	if !st.Setup.Complete {
		v.SetupToken = p.setupToken
	}
	v.Domain, v.TrustName, v.TrustURL = st.Domain, st.ProxyName, st.TrustURL
	v.Ready = st.Setup.Complete && st.Domain != ""
	if !v.Ready {
		return v
	}
	s.sayHello(ctx, p, self)
	hosts, err := p.hosts(ctx)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	v.Names = map[string]providerHost{}
	for _, h := range hosts {
		if name, ok := ownKey(h.Ref); ok {
			v.Names[name] = h
		}
	}
	v.Taken = takenNames(hosts)
	return v
}

// sayHello tells the provider who this fjord is (id, name, the address it
// is opened at), once per change: an installer paired before fjord had an
// id, or one renamed since, is then listed like every other fjord.
func (s *server) sayHello(ctx context.Context, p *provider, self Instance) {
	s.helloMu.Lock()
	origin := s.origin
	key := strings.Join([]string{p.base, self.ID, self.Label(), origin}, "|")
	said := s.hello == key
	s.helloMu.Unlock()
	if said {
		return
	}
	body := map[string]string{"id": self.ID, "name": self.Label(), "url": origin}
	if _, err := p.call(ctx, "PUT", "/api/v1/me", body, nil); err != nil {
		return // an older provider has no /me; nothing lost
	}
	s.helloMu.Lock()
	s.hello = key
	s.helloMu.Unlock()
}

// takenNames is every label on the provider that is not this fjord's own,
// sorted, each once.
func takenNames(hosts []providerHost) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hosts {
		if _, own := ownKey(h.Ref); own {
			continue
		}
		for _, n := range append([]string{h.Name}, h.ExtraNames...) {
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// handleNames is /api/names. GET: is there a provider, is it set up, and
// which apps have names. POST: give apps names.
func (s *server) handleNames(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		// The address this browser uses is the one a provider should show
		// as the way back here.
		if o := requestOrigin(r); o != "" {
			s.helloMu.Lock()
			s.origin = o
			s.helloMu.Unlock()
		}
		writeJSONValue(w, s.namesState(ctx))
	case http.MethodPost:
		s.addNames(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// nameRequest is one app to give a name. The address and port are the ones
// a browser opens it at (the page knows them, from the same rule as Open);
// the provider is on the same network as that browser.
type nameRequest struct {
	Stack   string `json:"stack"`
	Label   string `json:"label"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	HTTPS   bool   `json:"https"`
	// ownOrigin is the stack's x-fjord own_origin, read here, not taken
	// from the page.
	ownOrigin bool
}

type nameResult struct {
	Stack string        `json:"stack"`
	Host  *providerHost `json:"host,omitempty"`
	Error string        `json:"error,omitempty"`
}

// addNames is POST /api/names: give each app a name. Each target is probed
// from the provider first; one it cannot reach gets no name (a name that
// opens nothing is worse than none) and says why.
func (s *server) addNames(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []nameRequest `json:"names"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	p, err := s.findProvider(ctx)
	if err != nil || p == nil {
		http.Error(w, cmpErr(err, "nothing installed gives names"), http.StatusConflict)
		return
	}
	out := make([]nameResult, 0, len(req.Names))
	for _, n := range req.Names {
		stackName, _, _ := strings.Cut(n.Stack, "/")
		if st, err := s.manager.Get(stackName); err == nil {
			n.ownOrigin = wantsOwnOrigin(st)
		}
		res := p.name(ctx, n)
		if res.Host != nil {
			s.applyNameEnvLater(n.Stack)
		}
		out = append(out, res)
	}
	writeJSONValue(w, map[string]any{"results": out})
}

func cmpErr(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

func (p *provider) name(ctx context.Context, n nameRequest) nameResult {
	res := nameResult{Stack: n.Stack}
	if !validNameKey(n.Stack) {
		res.Error = "not an app"
		return res
	}
	var pr struct {
		Reachable     bool `json:"reachable"`
		HTTPS         bool `json:"https"`
		UntrustedCert bool `json:"untrustedCert"`
	}
	// The address is the one the browser reached fjord on. A provider on
	// another machine may not reach it (a host on two networks, opened from
	// the one the provider is not on): try this host's other addresses
	// before giving up, and name every address tried when none works.
	address := n.Address
	tried := []string{}
	for _, addr := range addressCandidates(n.Address, hostIPv4s()) {
		q := url.Values{"address": {addr}, "port": {strconv.Itoa(n.Port)}}
		if _, err := p.call(ctx, "GET", "/api/v1/probe?"+q.Encode(), nil, &pr); err != nil {
			res.Error = err.Error()
			return res
		}
		if pr.Reachable {
			address = addr
			break
		}
		tried = append(tried, fmt.Sprintf("%s:%d", addr, n.Port))
	}
	if !pr.Reachable {
		res.Error = fmt.Sprintf("%s cannot reach %s", p.Stack, strings.Join(tried, " or "))
		return res
	}
	body := map[string]any{
		"name":   nameLabel(cmpStr(n.Label, n.Stack)),
		"target": map[string]any{"address": address, "port": n.Port, "https": pr.HTTPS, "acceptUntrusted": pr.UntrustedCert},
		"access": "home",
		"ref":    nameRef(n.Stack),
	}
	if n.ownOrigin {
		body["ownOrigin"] = true
	}
	var h providerHost
	if _, err := p.call(ctx, "POST", "/api/v1/hosts", body, &h); err != nil {
		res.Error = err.Error()
		return res
	}
	res.Host = &h
	return res
}

// hostIPv4s is this host's own IPv4 addresses, loopback left out.
func hostIPv4s() []string {
	var out []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLoopback() {
			out = append(out, ipnet.IP.String())
		}
	}
	return out
}

// addressCandidates is where the provider is asked to reach the app, in
// order: the address the browser used, then this host's others when that
// was one of them. A loopback address is the browser sitting on the host
// itself (http://localhost:3567): no provider elsewhere can reach that, so
// the host's own addresses stand in for it from the start.
func addressCandidates(addr string, own []string) []string {
	if ip := net.ParseIP(addr); ip != nil && ip.IsLoopback() {
		if len(own) > 0 {
			return own
		}
		return []string{addr}
	}
	return append([]string{addr}, otherHostAddresses(addr, own)...)
}

// otherHostAddresses is the host's addresses worth trying after failed, in
// order: none unless failed is one of them, since an app on an address of
// its own is not on this host at all.
func otherHostAddresses(failed string, own []string) []string {
	mine := false
	for _, a := range own {
		if a == failed {
			mine = true
		}
	}
	if !mine {
		return nil
	}
	var out []string
	for _, a := range own {
		if a != failed {
			out = append(out, a)
		}
	}
	return out
}

func cmpStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// nameLabel makes a stack name into a name label: lower case, letters,
// digits and dashes, no dash at either end.
func nameLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == ' ' || r == '.':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// handleName is /api/names/<stack>. DELETE takes the app's name away (the
// provider keeps it restorable for a day). PUT {"label"} changes it: the
// host keeps its id, target and access, only the name is new.
func (s *server) handleName(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/api/names/")
	if !validNameKey(key) {
		http.Error(w, "not an app", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	switch r.Method {
	case http.MethodDelete:
		if err := s.forgetName(ctx, key); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.applyNameEnvLater(key)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPut:
		var req struct {
			Label string `json:"label"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", http.StatusBadRequest)
			return
		}
		if nameLabel(req.Label) == "" {
			http.Error(w, "not a name: letters, digits and dashes", http.StatusBadRequest)
			return
		}
		p, err := s.findProvider(ctx)
		if err != nil || p == nil {
			http.Error(w, cmpErr(err, "nothing installed gives names"), http.StatusConflict)
			return
		}
		h, err := p.rename(ctx, key, req.Label)
		if errors.Is(err, errNoName) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.applyNameEnvLater(key)
		writeJSONValue(w, h)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// errNoName: the app has no name to change.
var errNoName = errors.New("it has no name")

// rename gives one app's name (key: a stack, or "<stack>/<service>") a new
// label. The provider says no when the label is taken, in its own words.
func (p *provider) rename(ctx context.Context, key, label string) (providerHost, error) {
	hosts, err := p.hosts(ctx)
	if err != nil {
		return providerHost{}, err
	}
	ref := nameRef(key)
	for _, h := range hosts {
		if h.Ref != ref {
			continue
		}
		h.Name = nameLabel(label)
		if _, err := p.call(ctx, "PUT", "/api/v1/hosts/"+url.PathEscape(h.ID), h.input(), &h); err != nil {
			return providerHost{}, err
		}
		return h, nil
	}
	return providerHost{}, errNoName
}

// forgetName removes the names fjord gave key: one app's
// ("crafting-apps/photocraft"), or a stack's, which is all of its apps'.
func (s *server) forgetName(ctx context.Context, key string) error {
	p, err := s.findProvider(ctx)
	if err != nil || p == nil {
		return err
	}
	return p.forget(ctx, key)
}

// forget removes every name of one stack (or stack/app) from the provider.
func (p *provider) forget(ctx context.Context, key string) error {
	hosts, err := p.hosts(ctx)
	if err != nil {
		return err
	}
	// By ownKey, not the id form alone: a record written before this fjord
	// had an id carries its host name, and zensical's name outlived its
	// stack on netlab (2026-10-26) because only the id form was matched.
	for _, h := range hosts {
		k, own := ownKey(h.Ref)
		if own && (k == key || strings.HasPrefix(k, key+"/")) {
			if _, err := p.call(ctx, "DELETE", "/api/v1/hosts/"+url.PathEscape(h.ID), nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// forgetNamesAfterDelete is the delete hook: a deleted app's name goes too.
// Best effort and in the background; the delete has already succeeded.
func (s *server) forgetNamesAfterDelete(stackName string) {
	forgetProviderPin(s.fjordRoot, stackName)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.forgetName(ctx, stackName); err != nil {
			log.Printf("[names] %s deleted, but its name stays: %v", stackName, err)
		}
	}()
}

// forgetProviderPin drops the certificate fjord pinned for a names
// provider installed as this stack. A reinstalled provider makes a new
// root; the old pin then refused it for good ("its certificate is not the
// one fjord trusts", netlab 2026-10-26). Not a provider: there is no file.
func forgetProviderPin(fjordRoot, stackName string) {
	if stackName == "" || strings.ContainsAny(stackName, `/\`) {
		return
	}
	os.Remove(filepath.Join(fjordRoot, "secrets", "names-"+stackName+".json"))
}

func writeJSONValue(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// syncTargets keeps a stack's names pointing where the app is. Names are
// made with the address and port a browser opens the app at; a DHCP lease
// that moved, a stack put on another network, or a port changed on Apply
// left them pointing at nothing. Called from the status loop whenever
// where Open goes changed, with the stack's current status; best effort.
func (s *server) syncTargets(st *stack.Stack, status engine.StackStatus) {
	s.syncMu.Lock()
	if s.syncing == nil {
		s.syncing = map[string]bool{}
	}
	if s.syncing[st.Name] {
		s.syncMu.Unlock()
		return
	}
	s.syncing[st.Name] = true
	s.syncMu.Unlock()
	go func() {
		defer func() {
			s.syncMu.Lock()
			delete(s.syncing, st.Name)
			s.syncMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.syncTargetsNow(ctx, st, status); err != nil {
			log.Printf("[names] %s: %v", st.Name, err)
		}
	}()
}

func (s *server) syncTargetsNow(ctx context.Context, st *stack.Stack, status engine.StackStatus) error {
	p, err := s.findProvider(ctx)
	if err != nil || p == nil || p.Stack == st.Name {
		return err
	}
	return p.syncStack(ctx, st, status)
}

// syncStack re-points every name of one stack that no longer matches
// where the app is.
func (p *provider) syncStack(ctx context.Context, st *stack.Stack, status engine.StackStatus) error {
	hosts, err := p.hosts(ctx)
	if errors.Is(err, errUntrusted) {
		// Its certificate changed (a new ending, or a reset): status()
		// sorts the pin out; nobody has to open fjord's page for that.
		if _, _, err := p.status(ctx); err != nil {
			return err
		}
		hosts, err = p.hosts(ctx)
	}
	if err != nil {
		return err
	}
	views := stackServices(st, status)
	for _, h := range hosts {
		key, ok := ownKey(h.Ref)
		if !ok {
			continue
		}
		name, svc, _ := strings.Cut(key, "/")
		if name != st.Name {
			continue
		}
		addr, port := currentTarget(st, views, svc, h.Target.Port, ownAddress)
		if addr == "" {
			addr = h.Target.Address
		}
		if port == 0 || (addr == h.Target.Address && port == h.Target.Port) {
			continue
		}
		h.Target.Address, h.Target.Port = addr, port
		if _, err := p.call(ctx, "PUT", "/api/v1/hosts/"+url.PathEscape(h.ID), h.input(), nil); err != nil {
			return fmt.Errorf("%s is at %s:%d now, but its name was not updated: %v", key, addr, port, err)
		}
		log.Printf("[names] %s is at %s:%d now; its name follows", key, addr, port)
	}
	return nil
}

// currentTarget is where a browser opens the app now, by the same rule as
// Open: its own address on a reachable network and the port it answers on
// there (unchanged), else "" for the address, which is this host's and
// which only a browser knows, and the port its page is published on
// (x-fjord.web_port). A port of 0 means nothing is known: a stack still
// coming up, or one with no page.
func currentTarget(st *stack.Stack, views []serviceView, svc string, port int, reachable func(string) bool) (string, int) {
	if svc != "" {
		for _, v := range views {
			if v.Name == svc {
				if addr := linkHost([]serviceView{v}, reachable); addr != "" {
					return addr, port
				}
			}
		}
		return "", 0
	}
	if addr := linkHost(views, reachable); addr != "" {
		return addr, port
	}
	return "", webPortHint(st)
}

// wantsOwnOrigin is the stack's x-fjord own_origin: its app checks Origin
// and Referer against its own address (UniFi), so a names provider should
// send them as that. See docs/names-provider.md.
func wantsOwnOrigin(st *stack.Stack) bool {
	var x struct {
		XFjord struct {
			OwnOrigin bool `yaml:"own_origin"`
		} `yaml:"x-fjord"`
	}
	return yaml.Unmarshal([]byte(st.Compose), &x) == nil && x.XFjord.OwnOrigin
}

// webPortHint is the port the stack's page is published on, from
// x-fjord.web_port resolved through .env; 0 when the stack says nothing.
func webPortHint(st *stack.Stack) int {
	var x struct {
		XFjord struct {
			WebPort string `yaml:"web_port"`
		} `yaml:"x-fjord"`
	}
	if yaml.Unmarshal([]byte(st.Compose), &x) != nil {
		return 0
	}
	n, err := strconv.Atoi(fillVar(x.XFjord.WebPort, st.EnvMap()))
	if err != nil || n <= 0 || n > 65535 {
		return 0
	}
	return n
}
