package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Instance is this fjord as the apps it connects to see it: a permanent
// random id, and a name that starts as the host's.
type Instance struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Label is how a provider lists this fjord: "fjord on netlab".
func (i Instance) Label() string { return "fjord on " + i.Name }

// NamesLink is a names provider linked by hand: a reverse proxy elsewhere,
// reached at URL with Key (made by its Connected apps page for this fjord).
type NamesLink struct {
	URL       string `json:"url"`
	Key       string `json:"key"`
	ProxyName string `json:"proxyName,omitempty"`
}

// instanceID is this fjord's id, set at startup; refs carry it.
var instanceID string

// ensureInstance returns the saved identity, making one on the first run.
func ensureInstance(fjordRoot string) Instance {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	st := loadSettings(fjordRoot)
	if st.Instance != nil && st.Instance.ID != "" && st.Instance.Name != "" {
		return *st.Instance
	}
	var b [6]byte
	rand.Read(b[:])
	host, _ := os.Hostname()
	in := Instance{ID: hex.EncodeToString(b[:]), Name: strings.SplitN(host, ".", 2)[0]}
	if st.Instance != nil {
		if st.Instance.ID != "" {
			in.ID = st.Instance.ID
		}
		if st.Instance.Name != "" {
			in.Name = st.Instance.Name
		}
	}
	st.Instance = &in
	_ = saveSettings(fjordRoot, st)
	return in
}

func (s *server) instance() Instance { return ensureInstance(s.fjordRoot) }

// linkedProvider is the provider linked by hand, nil when none is.
func (s *server) linkedProvider() *provider {
	l := loadSettings(s.fjordRoot).NamesLink
	if l == nil || l.URL == "" || l.Key == "" {
		return nil
	}
	label := l.ProxyName
	if label == "" {
		if u, err := url.Parse(l.URL); err == nil {
			label = u.Host
		}
	}
	p := &provider{Stack: label, base: l.URL, key: l.Key, linked: true,
		pinPath: filepath.Join(s.fjordRoot, "secrets", "names-linked.json")}
	p.pin = loadPin(p.pinPath)
	return p
}

// handleNamesLink is /api/names/link. POST {link}: the link from the
// proxy's Connected apps page (https://proxy.apps.lan/link#code=…), or an
// address and a code; fjord redeems the code for a key of its own, pins
// the proxy's root, and from then on names work as with an installed
// provider. DELETE: forget it.
func (s *server) handleNamesLink(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var in struct {
			Link string `json:"link"`
			Code string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid JSON payload", 400)
			return
		}
		base, code, err := parseLink(in.Link, in.Code)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if s.findStackProvider(r.Context()) {
			http.Error(w, "a stack installed here already gives names; delete it to link a proxy elsewhere instead", 400)
			return
		}
		if err := s.linkProvider(r.Context(), base, code, requestOrigin(r)); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSONValue(w, s.namesState(r.Context()))
	case http.MethodDelete:
		settingsMu.Lock()
		st := loadSettings(s.fjordRoot)
		st.NamesLink = nil
		err := saveSettings(s.fjordRoot, st)
		settingsMu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		os.Remove(filepath.Join(s.fjordRoot, "secrets", "names-linked.json"))
		writeJSONValue(w, s.namesState(r.Context()))
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// findStackProvider reports whether one of this fjord's stacks provides names.
func (s *server) findStackProvider(ctx context.Context) bool {
	stacks, err := s.manager.List()
	if err != nil {
		return false
	}
	for _, st := range stacks {
		if s.namesOf(ctx, st) != nil {
			return true
		}
	}
	return false
}

// parseLink reads the proxy's address and the code out of what was pasted:
// the whole link, or an address with the code given beside it.
func parseLink(link, code string) (base, c string, err error) {
	link = strings.TrimSpace(link)
	if link == "" {
		return "", "", errors.New("paste the link from the proxy's Connected apps page")
	}
	if !strings.Contains(link, "://") {
		link = "https://" + link
	}
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return "", "", errors.New("that is not a link; paste the one from the proxy's Connected apps page")
	}
	c = strings.TrimSpace(code)
	if q, _ := url.ParseQuery(strings.TrimPrefix(u.Fragment, "#")); q.Get("code") != "" {
		c = q.Get("code")
	} else if u.Query().Get("code") != "" {
		c = u.Query().Get("code")
	}
	if c == "" {
		return "", "", errors.New("the link carries no code; copy it again from the proxy's Connected apps page, or type the code")
	}
	return "https://" + u.Host, strings.ToUpper(c), nil
}

// linkProvider redeems the code at the proxy and keeps what came back.
// The first call cannot be verified (the root is what we are about to
// learn); the code the person just copied is what makes it theirs.
func (s *server) linkProvider(ctx context.Context, base, code, origin string) error {
	self := s.instance()
	body, _ := json.Marshal(map[string]any{"code": code, "client": map[string]string{"id": self.ID, "name": self.Label(), "url": origin}})
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/api/v1/pair", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	cl := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}}
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %v", base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var out struct {
		Key       string `json:"key"`
		ProxyName string `json:"proxyName"`
		Domain    string `json:"domain"`
		Error     string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusCreated || out.Key == "" {
		if out.Error == "" {
			out.Error = fmt.Sprintf("%s answered %d", base, resp.StatusCode)
		}
		return errors.New(out.Error)
	}
	settingsMu.Lock()
	st := loadSettings(s.fjordRoot)
	st.NamesLink = &NamesLink{URL: base, Key: out.Key, ProxyName: out.ProxyName}
	err = saveSettings(s.fjordRoot, st)
	settingsMu.Unlock()
	if err != nil {
		return err
	}
	os.Remove(filepath.Join(s.fjordRoot, "secrets", "names-linked.json"))
	if p := s.linkedProvider(); p != nil && out.ProxyName != "" {
		if err := p.pinRoot(ctx, out.ProxyName); err != nil {
			return fmt.Errorf("linked, but cannot take its root certificate: %v", err)
		}
	}
	return nil
}

// handleNamesSelf is /api/names/self: PUT {name} renames this fjord as
// providers see it ("fjord on <name>").
func (s *server) handleNamesSelf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 40 {
		http.Error(w, "give it a short name", 400)
		return
	}
	settingsMu.Lock()
	st := loadSettings(s.fjordRoot)
	in2 := Instance{ID: instanceID, Name: name}
	if st.Instance != nil && st.Instance.ID != "" {
		in2.ID = st.Instance.ID
	}
	st.Instance = &in2
	err := saveSettings(s.fjordRoot, st)
	settingsMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// The provider hears the new name on the next look.
	s.helloMu.Lock()
	s.hello = ""
	s.helloMu.Unlock()
	writeJSONValue(w, in2)
}
