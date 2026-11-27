package hostnet

import (
	"os"
	"path/filepath"
	"testing"
)

// A host whose podman moved from CNI to netavark: new networks are read from
// netavark's directory and the conflists it left stay listed, marked legacy.
func TestSetBackend(t *testing.T) {
	cni, nv := t.TempDir(), t.TempDir()
	oldBak := netavarkBackupDir
	netavarkBackupDir = t.TempDir() // never the host's own
	t.Cleanup(func() { netavarkBackupDir = oldBak })
	oldCNI, oldNV := cniDir, netavarkDir
	mu.Lock()
	oldCur, oldPinned := cur, pinned
	pinned = false
	mu.Unlock()
	cniDir, netavarkDir = cni, nv
	t.Cleanup(func() {
		cniDir, netavarkDir = oldCNI, oldNV
		mu.Lock()
		cur, pinned = oldCur, oldPinned
		mu.Unlock()
	})
	write := func(dir, file, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(cni, "lan.conflist", `{"plugins":[{"type":"epair","master":"lanbr","ipam":{"type":"dhcp"}}]}`)
	write(cni, "both.conflist", `{"plugins":[{"type":"bridge","bridge":"cni-both"}]}`)
	write(nv, "priv.json", `{"name":"priv","driver":"bridge","network_interface":"podman1","subnets":[{"subnet":"10.89.0.0/24","gateway":"10.89.0.1"}]}`)
	write(nv, "both.json", `{"name":"both","driver":"bridge","network_interface":"podman2"}`)

	SetBackend("cni")
	if got := List(); len(got) != 2 || got[0].Name != "both" || got[0].Bridge != "cni-both" || got[1].Name != "lan" || got[1].Legacy {
		t.Errorf("cni: %+v", got)
	}
	if Path("x") != filepath.Join(cni, "x.conflist") || Dir() != cni {
		t.Errorf("cni path: %s", Path("x"))
	}

	SetBackend("netavark")
	got := List()
	if len(got) != 3 {
		t.Fatalf("netavark: %+v", got)
	}
	if got[0].Name != "both" || got[0].Bridge != "podman2" || got[0].Legacy {
		t.Errorf("a name in both formats is not the backend's: %+v", got[0])
	}
	if got[1].Name != "lan" || !got[1].Legacy || !got[1].DHCP || got[1].Bridge != "lanbr" {
		t.Errorf("legacy conflist: %+v", got[1])
	}
	if got[2].Name != "priv" || got[2].Legacy || got[2].Subnet != "10.89.0.0/24" {
		t.Errorf("netavark network: %+v", got[2])
	}
	if n, ok := Get("lan"); !ok || !n.Legacy {
		t.Errorf("Get legacy: %+v %v", n, ok)
	}
	if Path("x") != filepath.Join(nv, "x.json") {
		t.Errorf("netavark path: %s", Path("x"))
	}
	if ConflistPath("x") != filepath.Join(cni, "x.conflist") || ConflistDir() != cni {
		t.Errorf("conflist path under netavark: %s", ConflistPath("x"))
	}

	// Compose's per-project network is told from one somebody made, in both
	// formats: podman keeps the labels under args in a conflist.
	write(cni, "app_default.conflist", `{"args":{"podman_labels":{"io.podman.compose.project":"app"}},"plugins":[{"type":"bridge","bridge":"cni-podman3"}]}`)
	write(nv, "web_default.json", `{"name":"web_default","driver":"bridge","labels":{"com.docker.compose.project":"web"}}`)
	if n, _ := Get("app_default"); !n.Compose || !n.Legacy {
		t.Errorf("compose conflist: %+v", n)
	}
	if n, _ := Get("web_default"); !n.Compose || n.Legacy {
		t.Errorf("compose netavark network: %+v", n)
	}
	if n, _ := Get("lan"); n.Compose {
		t.Errorf("a LAN network read as compose's: %+v", n)
	}

	// A test's own layout is not moved by what podman answers meanwhile.
	dir := t.TempDir()
	restore := UseConflists(dir)
	SetBackend("cni")
	if Dir() != dir {
		t.Errorf("pinned layout moved to %s", Dir())
	}
	restore()
	if Dir() != nv {
		t.Errorf("restore: %s", Dir())
	}
}

// A host that went back to CNI: the networks it made under netavark, kept by
// netavark-migrate back, are listed as legacy; a name CNI has again is not.
func TestSetBackendBack(t *testing.T) {
	cni, nv, bak := t.TempDir(), t.TempDir(), t.TempDir()
	oldCNI, oldNV, oldBak := cniDir, netavarkDir, netavarkBackupDir
	mu.Lock()
	oldCur, oldPinned := cur, pinned
	pinned = false
	mu.Unlock()
	cniDir, netavarkDir, netavarkBackupDir = cni, nv, bak
	t.Cleanup(func() {
		cniDir, netavarkDir, netavarkBackupDir = oldCNI, oldNV, oldBak
		mu.Lock()
		cur, pinned = oldCur, oldPinned
		mu.Unlock()
	})
	write := func(dir, file, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(cni, "old.conflist", `{"plugins":[{"type":"bridge","bridge":"cni-old"}]}`)
	write(bak, "new.json", `{"name":"new","driver":"bridge","network_interface":"podman3","subnets":[{"subnet":"10.89.3.0/24","gateway":"10.89.3.1"}]}`)
	write(bak, "old.json", `{"name":"old","driver":"bridge","network_interface":"podman4"}`)

	SetBackend("cni")
	got := List()
	if len(got) != 2 || got[0].Name != "new" || !got[0].Legacy || got[0].Subnet != "10.89.3.0/24" || got[1].Name != "old" || got[1].Legacy {
		t.Errorf("back on cni: %+v", got)
	}
	if n, ok := Get("new"); !ok || !n.Legacy {
		t.Errorf("Get(new) = %+v, %v; want the backup's, legacy", n, ok)
	}
}
