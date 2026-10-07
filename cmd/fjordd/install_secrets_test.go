package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daemonless/fjord/pkg/manifest"
)

// Reinstalled over kept data: the saved secrets, not new ones, so MariaDB
// does not answer "access denied". A value typed at install still wins.
func TestSecretsReusedOnReinstall(t *testing.T) {
	dir := t.TempDir()
	m := &manifest.Manifest{Variables: []manifest.Var{
		{Name: "DB_PASSWORD", Type: "secret"}, {Name: "API_KEY", Type: "secret"}, {Name: "TZ", Type: "string"}}}
	if err := saveSecrets(dir, m, map[string]string{"DB_PASSWORD": "old", "API_KEY": "k", "TZ": "UTC"}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, secretsFile)); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file: %v", fi)
	}
	values := map[string]string{"API_KEY": "typed"}
	got := reuseSecrets(dir, values)
	if len(got) != 1 || got[0] != "DB_PASSWORD" || values["DB_PASSWORD"] != "old" || values["API_KEY"] != "typed" {
		t.Fatalf("reused %v, values %v", got, values)
	}
	if _, ok := values["TZ"]; ok {
		t.Fatal("only secrets are kept")
	}
}

// No data folder: nothing written, nothing created.
func TestSecretsNotSavedWithoutData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "none")
	m := &manifest.Manifest{Variables: []manifest.Var{{Name: "P", Type: "secret"}}}
	if err := saveSecrets(dir, m, map[string]string{"P": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("created the folder")
	}
}
