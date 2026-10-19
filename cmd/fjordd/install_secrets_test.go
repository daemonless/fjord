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
	root, dir := t.TempDir(), t.TempDir()
	m := &manifest.Manifest{Variables: []manifest.Var{
		{Name: "DB_PASSWORD", Type: "secret"}, {Name: "API_KEY", Type: "secret"}, {Name: "TZ", Type: "string"}}}
	if err := saveSecrets(root, dir, m, map[string]string{"DB_PASSWORD": "old", "API_KEY": "k", "TZ": "UTC"}); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"API_KEY": "typed"}
	got := reuseSecrets(root, dir, m, values)
	if len(got) != 1 || got[0] != "DB_PASSWORD" || values["DB_PASSWORD"] != "old" || values["API_KEY"] != "typed" {
		t.Fatalf("reused %v, values %v", got, values)
	}
	if _, ok := values["TZ"]; ok {
		t.Fatal("only secrets are kept")
	}
}

// Kept where the app cannot reach them (review 1.4): the app's folder is its
// container's, which could replace a file there with its own values.
func TestSecretsKeptOutOfTheAppsFolder(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	m := &manifest.Manifest{Variables: []manifest.Var{{Name: "P", Type: "secret"}}}
	os.WriteFile(filepath.Join(dir, secretsFile), []byte("P=from-before\n"), 0o600)
	if err := saveSecrets(root, dir, m, map[string]string{"P": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, secretsFile)); err == nil {
		t.Error("a secrets file is still in the app's folder")
	}
	fi, err := os.Stat(secretsPath(root, dir))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file: %v %v", fi, err)
	}
	if d, _ := os.Stat(filepath.Dir(secretsPath(root, dir))); d == nil || d.Mode().Perm() != 0o700 {
		t.Errorf("secrets folder: %v", d)
	}
}

// A file the container wrote (or one from before this change, in its folder)
// fills secrets only: not a path that would mount /etc into it, not PUID.
func TestSecretsReuseOnlySecrets(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(dir, secretsFile), []byte("PUID=0\nDATA=/etc\nDB_PASSWORD=kept\nCHOSEN=too\n"), 0o600)
	m := &manifest.Manifest{
		Variables: []manifest.Var{{Name: "PUID", Type: "string"}, {Name: "DATA", Type: "path", Optional: true}, {Name: "DB_PASSWORD", Type: "secret"}},
		Choices:   []manifest.Choice{{ID: "db", Options: []manifest.Option{{ID: "mariadb", Secrets: []string{"CHOSEN"}}}}},
	}
	values := map[string]string{}
	reuseSecrets(root, dir, m, values)
	if values["PUID"] != "" || values["DATA"] != "" || values["DB_PASSWORD"] != "kept" || values["CHOSEN"] != "too" {
		t.Errorf("values %v", values)
	}
}

// No data folder: nothing written, nothing created.
func TestSecretsNotSavedWithoutData(t *testing.T) {
	root, dir := t.TempDir(), filepath.Join(t.TempDir(), "none")
	m := &manifest.Manifest{Variables: []manifest.Var{{Name: "P", Type: "secret"}}}
	if err := saveSecrets(root, dir, m, map[string]string{"P": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("created the folder")
	}
	if _, err := os.Stat(secretsPath(root, dir)); err == nil {
		t.Fatal("kept secrets for a stack with no data")
	}
}
