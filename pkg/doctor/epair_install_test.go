package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The plugin is written only when it is exactly the pinned file; a wrong one
// never replaces what is there.
func TestInstallEpairChecksChecksum(t *testing.T) {
	good := []byte("#!/bin/sh\n# epair\n")
	sum := sha256.Sum256(good)
	served := good
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(served) }))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "libexec/cni/epair")
	oldURL, oldSum, oldDest := epairURL, epairSHA256, epairDest
	epairURL, epairSHA256, epairDest = srv.URL, hex.EncodeToString(sum[:]), dest
	t.Cleanup(func() { epairURL, epairSHA256, epairDest = oldURL, oldSum, oldDest })

	if err := installEpair(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed plugin: %v %v", info, err)
	}

	served = []byte("#!/bin/sh\nrm -rf /\n")
	if err := installEpair(context.Background()); err == nil {
		t.Fatal("a file that does not match the checksum was accepted")
	}
	if b, _ := os.ReadFile(dest); string(b) != string(good) {
		t.Errorf("a failed install replaced the working plugin: %q", b)
	}
}
