package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

const schemeCatalog = `{"apps":[
  {"id":"sparkyfitness","image":"ghcr.io/daemonless/sparkyfitness",
   "variants":[{"id":"latest"}]},
  {"id":"postgres","image":"ghcr.io/daemonless/postgres","variants":[
    {"id":"17","aliases":["17-pkg"]},
    {"id":"18","default":true,"aliases":["18-pkg","pkg","latest"]},
    {"id":"18-pkg-latest","aliases":["pkg-latest"]}]}
]}`

func seed(t *testing.T) *Cache {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(schemeCatalog), 0o644); err != nil {
		t.Fatal(err)
	}
	return New(dir, nil)
}

// Every variant id is a channel, each alias is one too, and each alias points
// at the variant it follows -- the whole scheme, not just the primary tags.
func TestTagSchemeDeclaredAliases(t *testing.T) {
	channels, aliases := seed(t).TagScheme("ghcr.io/daemonless/postgres")

	want := map[string]bool{"17": true, "17-pkg": true, "18": true, "18-pkg": true,
		"pkg": true, "latest": true, "18-pkg-latest": true, "pkg-latest": true}
	if len(channels) != len(want) {
		t.Fatalf("channels = %v, want %d entries", channels, len(want))
	}
	for _, c := range channels {
		if !want[c] {
			t.Errorf("unexpected channel %q in %v", c, channels)
		}
	}
	for alias, canon := range map[string]string{
		"17-pkg": "17", "18-pkg": "18", "pkg": "18", "latest": "18", "pkg-latest": "18-pkg-latest",
	} {
		if aliases[alias] != canon {
			t.Errorf("alias %q -> %q, want %q", alias, aliases[alias], canon)
		}
	}
	if _, ok := aliases["18"]; ok {
		t.Errorf("a canonical channel was recorded as an alias: %v", aliases)
	}
}

// The lookup is by image repo, so an app's scheme is found even when the stack
// asking pulls it as a dependency of some other app.
func TestTagSchemeMatchesByImageRepo(t *testing.T) {
	c := seed(t)
	if ch, _ := c.TagScheme("ghcr.io/daemonless/sparkyfitness"); len(ch) != 1 || ch[0] != "latest" {
		t.Errorf("sparkyfitness channels = %v, want [latest]", ch)
	}
	// Nothing claims a third-party image: the caller infers instead.
	if ch, al := c.TagScheme("docker.io/library/redis"); ch != nil || al != nil {
		t.Errorf("undeclared repo returned a scheme: %v %v", ch, al)
	}
	if ch, _ := c.TagScheme(""); ch != nil {
		t.Errorf("empty repo returned a scheme: %v", ch)
	}
}
