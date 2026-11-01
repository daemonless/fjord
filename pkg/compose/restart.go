package compose

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Restart policies, as compose spells them.
const (
	RestartUnlessStopped = "unless-stopped"
	RestartAlways        = "always"
	RestartOnFailure     = "on-failure"
	RestartNo            = "no"
)

// Restart is one service's restart policy. Retries caps on-failure; 0 is no
// cap.
type Restart struct {
	Policy  string `json:"policy"`
	Retries int    `json:"retries,omitempty"`
}

// ParseRestart reads a compose `restart:` value: "on-failure:3" is on-failure
// with 3 retries. False for anything compose does not know.
func ParseRestart(v string) (Restart, bool) {
	policy, n, hasN := strings.Cut(strings.TrimSpace(v), ":")
	switch policy {
	case RestartUnlessStopped, RestartAlways, RestartNo:
		return Restart{Policy: policy}, !hasN
	case RestartOnFailure:
		if !hasN {
			return Restart{Policy: policy}, true
		}
		retries, err := strconv.Atoi(n)
		if err != nil || retries < 0 {
			return Restart{}, false
		}
		return Restart{Policy: policy, Retries: retries}, true
	}
	return Restart{}, false
}

// String is the compose spelling.
func (r Restart) String() string {
	if r.Policy == RestartOnFailure && r.Retries > 0 {
		return fmt.Sprintf("%s:%d", r.Policy, r.Retries)
	}
	return r.Policy
}

// ServiceRestarts is each service's `restart:` as written; "" where it has
// none.
func ServiceRestarts(composeYAML string) map[string]string {
	out := map[string]string{}
	var doc yaml.Node
	if yaml.Unmarshal([]byte(composeYAML), &doc) != nil || len(doc.Content) == 0 {
		return out
	}
	services := mapGet(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(services.Content); i += 2 {
		v := ""
		if svc := services.Content[i+1]; svc.Kind == yaml.MappingNode {
			if n := mapGet(svc, "restart"); n != nil {
				v = n.Value
				if n.Tag == "!!bool" && (v == "false" || v == "no") {
					v = RestartNo // an unquoted YAML 1.1 `no`
				}
			}
		}
		out[services.Content[i].Value] = v
	}
	return out
}

// SetRestart writes r as every service's `restart:`. A service that says
// "no" while others do not is a one-shot (a migration, an init) and is left
// alone: under unless-stopped it would run again on every exit.
func SetRestart(composeYAML string, r Restart) (string, error) {
	if _, ok := ParseRestart(r.String()); !ok {
		return "", fmt.Errorf("unknown restart policy %q", r.Policy)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return "", err
	}
	if len(doc.Content) == 0 {
		return "", fmt.Errorf("empty compose")
	}
	services := mapGet(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose has no services")
	}
	current := ServiceRestarts(composeYAML)
	allNo := true
	for _, v := range current {
		allNo = allNo && v == RestartNo
	}
	for i := 0; i+1 < len(services.Content); i += 2 {
		svc := services.Content[i+1]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		if current[services.Content[i].Value] == RestartNo && !allNo {
			continue
		}
		// Quoted: an unquoted `no` is a boolean to a YAML 1.1 reader.
		mapSet(svc, "restart", quotedScalar(r.String()))
	}
	return encodeRoot(doc.Content[0])
}

// StackRestart is a stack's restart policy as one answer: the policy its
// services share, or Mixed with each one's own.
type StackRestart struct {
	Restart
	Mixed    bool              `json:"mixed,omitempty"`
	Services map[string]string `json:"services"`
}

// RestartOf sums up a stack's services. One-shot services ("no" beside
// others that restart) are left out, as SetRestart leaves them alone. A
// service with no `restart:` counts as its own answer: Policy "" when no
// service sets one, mixed beside services that do.
func RestartOf(composeYAML string) StackRestart {
	raw := ServiceRestarts(composeYAML)
	out := StackRestart{Services: raw}
	seen := map[string]bool{}
	oneShot := false
	for _, v := range raw {
		if v == RestartNo {
			oneShot = true
			continue
		}
		seen[v] = true
	}
	if oneShot && len(seen) == 0 {
		seen[RestartNo] = true
	}
	values := make([]string, 0, len(seen))
	for v := range seen {
		values = append(values, v)
	}
	sort.Strings(values)
	switch len(values) {
	case 0:
	case 1:
		out.Restart, _ = ParseRestart(values[0]) // "" (unset) stays the zero Restart
	default:
		out.Mixed = true
	}
	return out
}
