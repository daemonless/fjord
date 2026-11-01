package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Two catalogs carry an app of one id: daemonless's FreeBSD image and
// linuxserver's Linux one. Grouped by id, the higher-priority catalog's image
// led -- on Linux that was the FreeBSD one. An entry for another OS stays out
// of the merge; one that names no OS (an older catalog) is kept as before.
func TestMergedLeavesOutOtherOS(t *testing.T) {
	dir := t.TempDir()
	write := func(id, body string) {
		os.MkdirAll(filepath.Join(dir, id), 0o755)
		os.WriteFile(filepath.Join(dir, id, "catalog.json"), []byte(body), 0o644)
	}
	write("daemonless", `{"os":["freebsd"],"apps":[{"id":"jellyfin","image":"ghcr.io/daemonless/jellyfin"},{"id":"radarr","image":"ghcr.io/daemonless/radarr"}]}`)
	write("linuxserver", `{"apps":[{"id":"jellyfin","os":["linux"],"image":"lscr.io/linuxserver/jellyfin"}]}`)
	write("old", `{"apps":[{"id":"zensical","image":"ghcr.io/x/zensical"}]}`)
	c := New(dir, []Source{{ID: "daemonless", Name: "daemonless"}, {ID: "linuxserver", Name: "linuxserver"}, {ID: "old", Name: "old"}})
	merged := func(goos string) map[string]string {
		c.RunsOn = func(o []string) bool {
			for _, x := range o {
				if x == goos {
					return true
				}
			}
			return false
		}
		b, err := c.Merged()
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Apps []struct{ ID, Image string } `json:"apps"`
		}
		json.Unmarshal(b, &doc)
		out := map[string]string{}
		for _, a := range doc.Apps {
			out[a.ID] = a.Image
		}
		return out
	}
	if got := merged("linux"); got["jellyfin"] != "lscr.io/linuxserver/jellyfin" || got["radarr"] != "" || got["zensical"] == "" {
		t.Errorf("linux: %v", got)
	}
	if got := merged("freebsd"); got["jellyfin"] != "ghcr.io/daemonless/jellyfin" || got["radarr"] == "" || got["zensical"] == "" {
		t.Errorf("freebsd: %v", got)
	}
}
