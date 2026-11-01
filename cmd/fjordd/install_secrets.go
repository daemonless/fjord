package main

import (
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/daemonless/fjord/pkg/manifest"
)

// secretsFile is where secrets were kept before 0.4: in the stack's own data
// folder. That folder is its container's (uid 1000), which could replace the
// file with values of its own (review 1.4); it is read once, filtered, and
// moved to fjord's own secrets folder.
const secretsFile = ".fjord-secrets"

// secretsPath is where the secrets a data folder was set up with are kept:
// in a root-only folder under fjord's root, named for that data folder, so
// they outlive the stack when its data is kept on delete.
func secretsPath(root, dataDir string) string {
	return filepath.Join(root, "secrets", url.PathEscape(filepath.Clean(dataDir))+".env")
}

// secretNames are the variables m may hold secrets in: its own secrets, and
// those any of its choices' options adds.
func secretNames(m *manifest.Manifest) map[string]bool {
	out := map[string]bool{}
	for _, v := range m.Variables {
		if v.Type == "secret" {
			out[v.Name] = true
		}
	}
	for _, c := range m.Choices {
		for _, o := range c.Options {
			for _, s := range o.Secrets {
				out[s] = true
			}
		}
	}
	return out
}

// reuseSecrets fills each secret left empty in values from what dataDir was
// set up with, and names the ones it filled. Only m's secrets: anything else
// in the file -- a path, PUID -- is not taken.
func reuseSecrets(root, dataDir string, m *manifest.Manifest, values map[string]string) []string {
	b, err := os.ReadFile(secretsPath(root, dataDir))
	if err != nil {
		if b, err = os.ReadFile(filepath.Join(dataDir, secretsFile)); err != nil {
			return nil
		}
	}
	names := secretNames(m)
	saved, _ := parseEnvOrdered(string(b))
	var out []string
	for k, v := range saved {
		if names[k] && v != "" && values[k] == "" {
			values[k] = v
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// saveSecrets keeps the install's secrets for dataDir, when the stack keeps
// data there.
func saveSecrets(root, dataDir string, m *manifest.Manifest, env map[string]string) error {
	var lines []string
	for _, v := range m.Variables {
		if v.Type == "secret" && env[v.Name] != "" {
			lines = append(lines, v.Name+"="+env[v.Name])
		}
	}
	if len(lines) == 0 {
		return nil
	}
	if _, err := os.Stat(dataDir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	slices.Sort(lines)
	path := secretsPath(root, dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	os.Remove(filepath.Join(dataDir, secretsFile)) // the copy from before 0.4
	return nil
}
