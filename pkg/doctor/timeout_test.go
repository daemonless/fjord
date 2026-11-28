package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketProbeTimeoutIsUnknownNotFail(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "podman.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oldPath := podmanSocketPath
	oldPing := podmanPing
	t.Cleanup(func() {
		podmanSocketPath = oldPath
		podmanPing = oldPing
	})
	podmanSocketPath = func() string { return sock }

	// A deadline timeout under heavy load must report Unknown, not Fail.
	podmanPing = func(context.Context) error { return context.DeadlineExceeded }
	st, detail := socketProbe(context.Background())
	if st != Unknown || !strings.Contains(detail, "in time") {
		t.Fatalf("timeout probe = (%v, %q), want Unknown", st, detail)
	}

	// A real socket error (e.g. connection refused) still reports Fail.
	podmanPing = func(context.Context) error { return errors.New("connection refused") }
	st, detail = socketProbe(context.Background())
	if st != Fail || !strings.Contains(detail, "connection refused") {
		t.Fatalf("refused probe = (%v, %q), want Fail", st, detail)
	}
}

func TestRunDowngradesTimeoutToUnknown(t *testing.T) {
	p := platformInfo{os: "freebsd", mode: "host", canInstall: true}
	chk := Check{
		ID:   "pf",
		Name: "pf firewall",
		Probe: func(context.Context) (Status, string) {
			return Fail, "pf is not loaded"
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := p.run(ctx, chk)
	if res.Status != Unknown {
		t.Fatalf("canceled ctx status = %v, want Unknown (detail=%q)", res.Status, res.Detail)
	}
}
