package main

import (
	"context"
	"io"
	"log"
	"maps"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/stack"
)

// name_env (x-fjord): variables an app takes its names in. Some apps refuse
// a name nobody told them (SABnzbd answers "Hostname verification failed"
// behind the proxy) or need their own address (a base URL). When a names
// provider gives a stack a name, or changes or takes it away, fjord fills
// these from templates -- {name} is the main name, {names} every name,
// comma-separated, {proxy} where the provider connects from (an app that
// trusts only its proxy: Home Assistant's trusted_proxies) -- and recreates
// the app so it reads them. fjord knows nothing about the apps: the hint and
// what the variable means are theirs.

// nameEnvHints is the stack's x-fjord.name_env; for a stack whose compose
// predates the hint (installed or adopted earlier), its catalog app's.
func (s *server) nameEnvHints(ctx context.Context, st *stack.Stack) map[string]string {
	var x struct {
		XFjord struct {
			NameEnv map[string]string `yaml:"name_env"`
		} `yaml:"x-fjord"`
	}
	if yaml.Unmarshal([]byte(st.Compose), &x) == nil && len(x.XFjord.NameEnv) > 0 {
		return validNameEnv(x.XFjord.NameEnv)
	}
	if st.State == nil || st.State.Origin.AppID == "" || s.cat == nil {
		return nil
	}
	raw, err := s.cat.ManifestAny(ctx, st.State.Origin.AppID)
	if err != nil {
		return nil
	}
	m, err := manifest.Parse(string(raw))
	if err != nil {
		return nil
	}
	return validNameEnv(m.NameEnv)
}

func validNameEnv(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if manifest.EnvName(k) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stackNames are the full names the provider gives a stack (any of its
// services), main names first, in a stable order.
func stackNames(hosts []providerHost, stackName string) []string {
	var main, extra []string
	for _, h := range hosts {
		key, ok := ownKey(h.Ref)
		if !ok {
			continue
		}
		if st, _, _ := strings.Cut(key, "/"); st != stackName {
			continue
		}
		u, err := url.Parse(h.URL)
		if err != nil || u.Hostname() == "" {
			continue
		}
		full := u.Hostname()
		main = append(main, full)
		// Extra names are labels under the same ending.
		if domain, ok := strings.CutPrefix(full, h.Name+"."); ok {
			for _, e := range h.ExtraNames {
				extra = append(extra, e+"."+domain)
			}
		}
	}
	slices.Sort(main)
	slices.Sort(extra)
	return slices.Compact(append(main, extra...))
}

// fillNameEnv turns the templates into values for these names and the
// provider's sources.
func fillNameEnv(hints map[string]string, names, proxies []string) map[string]string {
	first := ""
	if len(names) > 0 {
		first = names[0]
	}
	out := map[string]string{}
	for k, tmpl := range hints {
		v := strings.ReplaceAll(tmpl, "{names}", strings.Join(names, ","))
		v = strings.ReplaceAll(v, "{proxy}", strings.Join(proxies, ","))
		out[k] = strings.ReplaceAll(v, "{name}", first)
	}
	return out
}

func wantsProxy(hints map[string]string) bool {
	for _, t := range hints {
		if strings.Contains(t, "{proxy}") {
			return true
		}
	}
	return false
}

// proxySources are where the provider's requests come from, as an app sees
// them. One on this host: the subnet holding each of its addresses, since its
// address changes when it is recreated and its network does not (the engine's
// networks, and every defined one: a stack's default bridge is only among
// the latter); an address in no known subnet is itself. A linked one: its
// host, which is what its traffic leaves from.
func (s *server) proxySources(ctx context.Context, p *provider) []string {
	if p.linked {
		u, err := url.Parse(p.base)
		if err != nil || u.Hostname() == "" {
			return nil
		}
		if ip := net.ParseIP(u.Hostname()); ip != nil {
			return []string{ip.String()}
		}
		addrs, _ := net.DefaultResolver.LookupHost(ctx, u.Hostname())
		slices.Sort(addrs)
		return addrs
	}
	st, err := s.manager.Get(p.Stack)
	if err != nil {
		return nil
	}
	be := s.backendFor(st)
	status, err := be.Status(ctx, st)
	if err != nil {
		return nil
	}
	var subnets []string
	if nets, err := be.Networks(ctx); err == nil {
		for _, n := range nets {
			subnets = append(subnets, n.Subnet)
		}
	}
	for _, n := range hostnet.List() {
		subnets = append(subnets, n.Subnet)
	}
	var out []string
	for _, v := range stackServices(st, status) {
		addrs := slices.Collect(maps.Values(v.Live))
		if len(addrs) == 0 {
			addrs = []string{v.Address}
		}
		for _, a := range addrs {
			if a != "" {
				out = append(out, subnetOf(a, subnets))
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// subnetOf is the first of subnets holding addr, or addr when none does.
func subnetOf(addr string, subnets []string) string {
	ip := net.ParseIP(addr)
	for _, sub := range subnets {
		if _, n, err := net.ParseCIDR(sub); err == nil && ip != nil && n.Contains(ip) {
			return n.String()
		}
	}
	return addr
}

// setEnvValues sets KEY=VALUE lines in .env text, replacing a key's line
// where it has one; reports whether anything changed.
func setEnvValues(env string, vals map[string]string) (string, bool) {
	lines := strings.Split(strings.TrimRight(env, "\n"), "\n")
	if env == "" {
		lines = nil
	}
	seen := map[string]bool{}
	changed := false
	for i, line := range lines {
		k, old, ok := strings.Cut(strings.TrimSpace(line), "=")
		v, want := vals[strings.TrimSpace(k)]
		if !ok || !want {
			continue
		}
		seen[strings.TrimSpace(k)] = true
		if strings.TrimSpace(old) != v {
			lines[i] = strings.TrimSpace(k) + "=" + v
			changed = true
		}
	}
	for _, k := range slices.Sorted(maps.Keys(vals)) {
		if !seen[k] {
			lines = append(lines, k+"="+vals[k])
			changed = true
		}
	}
	return strings.Join(lines, "\n") + "\n", changed
}

// applyNameEnvLater runs applyNameEnv in the background: naming has already
// succeeded, and the app's recreate is not the caller's to wait for.
func (s *server) applyNameEnvLater(stackKey string) {
	stackName, _, _ := strings.Cut(stackKey, "/")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := s.applyNameEnv(ctx, stackName); err != nil {
			log.Printf("[names] %s: name_env: %v", stackName, err)
		}
	}()
}

// applyNameEnv fills a stack's name_env from the names it has now, passes
// the variables to its service, and recreates it when it runs. A stack busy
// with something else keeps the change pending: its next start or Apply
// recreates it.
func (s *server) applyNameEnv(ctx context.Context, stackName string) error {
	st, err := s.manager.Get(stackName)
	if err != nil {
		return err
	}
	hints := s.nameEnvHints(ctx, st)
	if hints == nil {
		return nil
	}
	p, err := s.findProvider(ctx)
	if err != nil || p == nil {
		return err
	}
	hosts, err := p.hosts(ctx)
	if err != nil {
		return err
	}
	var proxies []string
	if wantsProxy(hints) {
		proxies = s.proxySources(ctx, p)
	}
	vals := fillNameEnv(hints, stackNames(hosts, stackName), proxies)

	unlock, ok := lockStackAs(stackName, "up")
	if !ok {
		unlock = func() {}
	}
	defer unlock()
	st, err = s.manager.Get(stackName)
	if err != nil {
		return err
	}
	env, changed := setEnvValues(st.Env, vals)
	keys := slices.Sorted(maps.Keys(vals))
	compose, err := composepkg.PassEnv(st.Compose, composepkg.FirstService, keys)
	if err != nil {
		return err
	}
	if !changed && compose == st.Compose {
		return nil
	}
	oldCompose, oldEnv := st.Compose, st.Env
	st.Compose, st.Env = compose, env
	if err := s.manager.Replace(st, func(cur *stack.Stack) error { return nil }); err != nil {
		return err
	}
	log.Printf("[names] %s: set %s", stackName, strings.Join(keys, ", "))
	pending := composepkg.ChangedServices(oldCompose, envMap(oldEnv), st.Compose, st.EnvMap())
	s.recordPending(stackName, pending)
	// The mark after the record: the recreate below applies this change,
	// and only what was recorded before the mark leaves the pending list.
	// Taken first, the change stayed "pending" and the stack page asked to
	// apply what the recreate had just applied.
	read := s.pending.now()
	if !ok || len(pending) == 0 || st.State == nil || st.State.DesiredState != "running" {
		return nil
	}
	rec, can := s.backendFor(st).(engine.Recreator)
	if !can {
		return nil
	}
	stream, err := rec.Recreate(watchable(ctx, stackName), st, pending)
	if err != nil {
		return err
	}
	stream = s.clearPendingAfter(stackName, pending, read, stream)
	io.Copy(io.Discard, stream)
	stream.Close()
	log.Printf("[names] %s: recreated with its names", stackName)
	return nil
}
