package main

import (
	"regexp"
	"strings"
)

// secretVarName is the UI's isSecret (composeVars.ts): a variable whose value
// is not shown on screen.
var secretVarName = regexp.MustCompile(`(?i)pass|pwd|secret|token|key|credential|auth`)
var urlUserPass = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^:@/\s]+):([^@/\s]+)@`)

// redactSecrets empties the value of every secret-named KEY=value line in a
// .env text (plus any explicitly listed secret variable names) and strips
// inline URL credentials; every other line stays as written.
func redactSecrets(env string, extra ...map[string]bool) string {
	lines := strings.Split(env, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		if !ok {
			continue
		}
		isExtra := false
		for _, m := range extra {
			if m[k] {
				isExtra = true
				break
			}
		}
		if isExtra || secretVarName.MatchString(k) {
			lines[i] = k + "="
		} else if urlUserPass.MatchString(v) {
			lines[i] = k + "=" + urlUserPass.ReplaceAllString(v, "${1}:@")
		}
	}
	return strings.Join(lines, "\n")
}
