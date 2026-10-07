package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/stack"
)

// A database keeps the password it was created with: changing it in .env
// after the first start changes what the app sends, not what the database
// expects, and the app is refused ("Access denied", "password
// authentication failed") with nothing saying why. The same goes for any
// secret an app stores the first time it runs. So a Save that moves one away
// from what the stack was set up with says so -- and one that puts it back
// does not.

// secretVars are the variables the stack's catalog app declares secret, its
// choices applied (a database option adds its password as one). Empty for a
// stack with no catalog app: nothing is guessed from names.
func (s *server) secretVars(ctx context.Context, st *stack.Stack) map[string]bool {
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
	// The stack's own values, so an option that asks for fields is answered.
	m.ApplyChoices(st.State.Choices, st.EnvMap())
	out := map[string]bool{}
	for _, v := range m.Variables {
		if v.Type == "secret" {
			out[v.Name] = true
		}
	}
	return out
}

func secretSum(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

// secretBaseline is sums with a sum added, from env, for each secret it has
// none for: what install recorded, else the value before this Save -- for a
// stack installed before sums were kept, the best there is.
func secretBaseline(sums map[string]string, env map[string]string, secrets map[string]bool) map[string]string {
	out := map[string]string{}
	for k, v := range sums {
		out[k] = v
	}
	for name := range secrets {
		if _, ok := out[name]; !ok && env[name] != "" {
			out[name] = secretSum(env[name])
		}
	}
	return out
}

// secretChangeWarnings says, for each secret whose value is no longer the
// one the stack was set up with, why the app may now be refused. Nothing for
// a stack that has never run: no database yet to disagree.
func secretChangeWarnings(baseline, newEnv map[string]string, secrets map[string]bool, hasRun bool) []string {
	if !hasRun {
		return nil
	}
	var names []string
	for name := range secrets {
		if sum, ok := baseline[name]; ok && secretSum(newEnv[name]) != sum {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		out = append(out, n+" changed: the app and its database were set up with the old value and keep it, so it may now be refused (\"access denied\"). Put the old value back, or change it inside the database too.")
	}
	return out
}
