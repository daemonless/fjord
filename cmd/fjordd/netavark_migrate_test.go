package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeMigrate(t *testing.T, answers map[string]struct {
	out  string
	code int
}) {
	t.Helper()
	oldTool, oldRun := migrateTool, runMigrate
	migrateTool = filepath.Join(t.TempDir(), "netavark-migrate")
	os.WriteFile(migrateTool, []byte("#!/bin/sh\n"), 0o755)
	runMigrate = func(_ context.Context, args ...string) ([]byte, int, error) {
		a := answers[strings.Join(args, " ")]
		return []byte(a.out), a.code, nil
	}
	t.Cleanup(func() { migrateTool, runMigrate = oldTool, oldRun })
}

type ans = struct {
	out  string
	code int
}

// Only an interface that does what fjord needs is used.
func TestMigrateUsable(t *testing.T) {
	ctx := context.Background()
	for v, want := range map[string]bool{
		"netavark-migrate 2 host json no-start\n": true,
		"netavark-migrate 3 host json\n":          true,
		"netavark-migrate 1\n":                    false,
		"netavark-migrate 2 json\n":               false,
		"something else 2 host json\n":            false,
	} {
		fakeMigrate(t, map[string]ans{"version": {v, 0}})
		if got := migrateUsable(ctx); got != want {
			t.Errorf("%q: %v", v, got)
		}
	}
	// Not installed.
	fakeMigrate(t, map[string]ans{"version": {"netavark-migrate 2 host json\n", 0}})
	migrateTool = "/nonexistent/netavark-migrate"
	if migrateUsable(ctx) {
		t.Error("used a tool that is not there")
	}
}

// A run that changed things says each; one that did part of it still
// reports, with its exit code.
func TestMigrateHostReport(t *testing.T) {
	fakeMigrate(t, map[string]ans{"host --json": {`{"version":2,"ok":false,"exit_code":2,"changed":true,
		"pf":{"changed":true,"status":"updated"},"sysctl":{"changed":true},"loader":{"changed":false},
		"dns_bind_port":{"changed":true,"status":"updated","port":5300,"file":"/usr/local/etc/containers/containers.conf.d/50-netavark-dns.conf"},
		"errors":["pfctl -f failed"],"backups":{"pf_conf":"/var/db/netavark-migrate/pf.conf.pre-netavark"}}`, 2}})
	rep, err := migrateHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	did := strings.Join(rep.did(), "\n")
	for _, want := range []string{"kept as /var/db/netavark-migrate/pf.conf.pre-netavark", "net.pf.filter_local", "listens on 5300 (set in /usr/local/etc/containers/containers.conf.d/50-netavark-dns.conf)"} {
		if !strings.Contains(did, want) {
			t.Errorf("no %q in:\n%s", want, did)
		}
	}
	if strings.Contains(did, "loader") || rep.ExitCode != 2 || rep.problems() != "pfctl -f failed" {
		t.Errorf("exit %d, problems %q, did:\n%s", rep.ExitCode, rep.problems(), did)
	}

	fakeMigrate(t, map[string]ans{"host --json": {"not json", 1}})
	if _, err := migrateHost(context.Background()); err == nil {
		t.Error("no report taken for one")
	}
}
