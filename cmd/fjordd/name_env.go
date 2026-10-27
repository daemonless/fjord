package main

import (
	"context"
	"io"
	"log"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/stack"
)

// name_env (x-fjord): variables an app takes its names in. Some apps refuse
// a name nobody told them (SABnzbd answers "Hostname verification failed"
// behind the proxy) or need their own address (a base URL). When a names
// provider gives a stack a name, or changes or takes it away, fjord fills
// these from templates -- {name} is the main name, {names} every name,
// comma-separated -- and recreates the app so it reads them. fjord knows
// nothing about the apps: the hint and what the variable means are theirs.

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

// fillNameEnv turns the templates into values for these names.
func fillNameEnv(hints map[string]string, names []string) map[string]string {
	first := ""
	if len(names) > 0 {
		first = names[0]
	}
	out := map[string]string{}
	for k, tmpl := range hints {
		v := strings.ReplaceAll(tmpl, "{names}", strings.Join(names, ","))
		out[k] = strings.ReplaceAll(v, "{name}", first)
	}
	return out
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
	vals := fillNameEnv(hints, stackNames(hosts, stackName))

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
