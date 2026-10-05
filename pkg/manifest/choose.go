package manifest

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"sort"

	composepkg "github.com/daemonless/fjord/pkg/compose"
)

// ApplyChoices answers the manifest's choices: picks maps a choice id to an
// option id, and a choice not in picks takes its default. The compose is
// rewritten (services dropped, services added, the app made to wait for a
// database it brought), the variables grow what the options ask for and
// seed, and values gets the options' env, their defaults where nothing was
// typed, and a made-up secret where one was left empty.
//
// Returns the answers in full, default included, for the stack's record.
func (m *Manifest) ApplyChoices(picks map[string]string, values map[string]string) (map[string]string, error) {
	answers := map[string]string{}
	if len(m.Choices) == 0 {
		return answers, nil
	}
	known := map[string]*Var{}
	for i := range m.Variables {
		known[m.Variables[i].Name] = &m.Variables[i]
	}
	var drop []string
	for _, c := range m.Choices {
		id := picks[c.ID]
		if id == "" {
			id = c.Default
		}
		o := c.Option(id)
		if o == nil {
			return nil, fmt.Errorf("%s: no option %q (one of %s)", c.Label, id, optionIDs(c))
		}
		answers[c.ID] = o.ID
		drop = append(drop, o.Drop...)
		// The services it adds join the stack's hostnames, so the install
		// can point their variables where they are reachable.
		for svc, v := range o.Hostnames {
			if m.Hostnames == nil {
				m.Hostnames = map[string]string{}
			}
			if _, ok := m.Hostnames[svc]; !ok {
				m.Hostnames[svc] = v
			}
		}
		// The option's env is the answer itself (the database type, the
		// host); it wins over anything typed.
		for k, v := range o.Env {
			values[k] = v
			if known[k] == nil {
				m.Variables = append(m.Variables, Var{Name: k, Type: "string", Optional: true})
				known[k] = &m.Variables[len(m.Variables)-1]
			}
		}
		// Defaults seed what the added services need, unless typed over.
		for _, k := range sortedKeys(o.Defaults) {
			if values[k] == "" {
				values[k] = o.Defaults[k]
			}
			if known[k] == nil {
				m.Variables = append(m.Variables, Var{Name: k, Type: "string", Optional: true})
				known[k] = &m.Variables[len(m.Variables)-1]
			}
		}
		for _, k := range o.Secrets {
			if values[k] == "" {
				values[k] = randomSecret(24)
			}
			if known[k] == nil {
				m.Variables = append(m.Variables, Var{Name: k, Type: "secret", Optional: true})
				known[k] = &m.Variables[len(m.Variables)-1]
			}
		}
		// What the option asks for is required, and typed by the person.
		for _, a := range o.Ask {
			typ := a.Type
			if typ == "" {
				typ = "string"
			}
			if v := known[a.Name]; v != nil {
				v.Optional = false
				if typ == "secret" {
					v.Type = typ
				}
			} else {
				m.Variables = append(m.Variables, Var{Name: a.Name, Type: typ, Default: a.Default})
				known[a.Name] = &m.Variables[len(m.Variables)-1]
			}
			if values[a.Name] == "" && a.Default != "" {
				values[a.Name] = a.Default
			}
			if values[a.Name] == "" {
				return nil, fmt.Errorf("%s: %s needs %s", c.Label, o.Label, firstNonEmpty(a.Label, a.Name))
			}
		}
	}
	compose, err := composepkg.DropServices(m.compose, drop)
	if err != nil {
		return nil, err
	}
	for _, c := range m.Choices {
		o := c.Option(answers[c.ID])
		if compose, err = composepkg.AddServices(compose, o.Services); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", c.Label, o.Label, err)
		}
		for _, svc := range sortedKeys(o.DependsOn) {
			if compose, err = composepkg.AddDependsOn(compose, svc, o.DependsOn[svc]); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", c.Label, o.Label, err)
			}
		}
	}
	m.compose = compose
	return answers, nil
}

func optionIDs(c Choice) string {
	s := ""
	for i, o := range c.Options {
		if i > 0 {
			s += ", "
		}
		s += o.ID
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// randomSecret is letters and digits only: every database image's password
// variable takes that, and nothing has to be quoted in .env.
func randomSecret(n int) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, n)
	for i := range out {
		x, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err) // the system's randomness is gone; nothing sane to do
		}
		out[i] = alphabet[x.Int64()]
	}
	return string(out)
}
