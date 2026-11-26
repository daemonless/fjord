//go:build freebsd

package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A service rc called failed but that comes up a moment later is up; one
// that never does is not.
func TestServiceUp(t *testing.T) {
	bin := t.TempDir()
	count := filepath.Join(t.TempDir(), "n")
	// A fake `service`: "not running" for its first three asks, then running.
	script := "#!/bin/sh\nn=$(cat " + count + " 2>/dev/null || echo 0); n=$((n+1)); echo $n > " + count + "\n" +
		"if [ \"$1\" = never ] || [ $n -le 3 ]; then echo \"$1 is not running.\"; exit 1; fi\necho \"$1 is running as pid 42.\"\n"
	if err := os.WriteFile(filepath.Join(bin, "service"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	ctx := context.Background()
	if !serviceUp(ctx, "podman_service", 5*time.Second) {
		t.Error("a service up on the fourth ask was called down")
	}
	start := time.Now()
	if serviceUp(ctx, "never", 1*time.Second) {
		t.Error("a service that never runs was called up")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("gave up after %v, past its wait", time.Since(start))
	}
}
