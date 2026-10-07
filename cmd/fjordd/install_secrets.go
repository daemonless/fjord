package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/daemonless/fjord/pkg/manifest"
)

// secretsFile sits in a stack's own data folder: the secrets its data was
// set up with, outliving the stack when its data is kept on delete.
const secretsFile = ".fjord-secrets"

// reuseSecrets fills each secret left empty in values from dir's
// secretsFile, and names the ones it filled.
func reuseSecrets(dir string, values map[string]string) []string {
	b, err := os.ReadFile(filepath.Join(dir, secretsFile))
	if err != nil {
		return nil
	}
	saved, _ := parseEnvOrdered(string(b))
	var out []string
	for k, v := range saved {
		if v != "" && values[k] == "" {
			values[k] = v
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// saveSecrets writes the install's secrets into dir, when the stack keeps
// data there.
func saveSecrets(dir string, m *manifest.Manifest, env map[string]string) error {
	var lines []string
	for _, v := range m.Variables {
		if v.Type == "secret" && env[v.Name] != "" {
			lines = append(lines, v.Name+"="+env[v.Name])
		}
	}
	if len(lines) == 0 {
		return nil
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	slices.Sort(lines)
	return os.WriteFile(filepath.Join(dir, secretsFile), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}
