package main

import (
	"regexp"
	"strings"
)

// secretVarName is the UI's isSecret (composeVars.ts): a variable whose value
// is not shown on screen.
var secretVarName = regexp.MustCompile(`(?i)pass|secret|token|key|credential|auth`)

// redactSecrets empties the value of every secret-named KEY=value line in a
// .env text; every other line stays as written.
func redactSecrets(env string) string {
	lines := strings.Split(env, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, _, ok := strings.Cut(t, "=")
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		if ok && secretVarName.MatchString(k) {
			lines[i] = k + "="
		}
	}
	return strings.Join(lines, "\n")
}
