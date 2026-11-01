package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/daemonless/fjord/pkg/engine/podman"
)

// binProbe checks a binary is reachable in PATH; ok detail is the resolved
// path so the operator can spot a wrong/shadowed install.
func binProbe(bin string) func(context.Context) (Status, string) {
	return func(context.Context) (Status, string) {
		p, err := exec.LookPath(bin)
		if err != nil {
			return Fail, bin + " not found in PATH"
		}
		return OK, p
	}
}

// socketProbe verifies the podman API socket exists AND answers. The split
// matters: an existing-but-dead socket is the classic stale-service failure
// (podman upgraded under a running API service) and needs a restart, not an
// install.
func socketProbe(ctx context.Context) (Status, string) {
	path := podman.SocketPath()
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Fail, fmt.Sprintf("socket %s does not exist -- is the podman API service running?", path)
		}
		return Fail, fmt.Sprintf("socket %s is not accessible: %v", path, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := podman.Ping(ctx); err != nil {
		return Fail, fmt.Sprintf("socket %s exists but is not answering (stale service from before a podman upgrade?): %v", path, err)
	}
	return answeringSocket(path, podmanInstalled())
}

// podmanInstalled says whether the podman command is there; a variable so
// the leftover-service verdict can be tested without removing podman.
var podmanInstalled = func() bool {
	_, err := exec.LookPath("podman")
	return err == nil
}

// answeringSocket is the verdict on a socket that answers. With podman gone
// it is a leftover: the API service outlives `pkg delete podman`, keeps
// answering, and Setup said "ok" for an engine that could not run a thing.
func answeringSocket(path string, installed bool) (Status, string) {
	if !installed {
		return Fail, fmt.Sprintf("socket %s answers, but podman is not installed: it is an API service left running from before podman was removed -- stop it, or reinstall podman", path)
	}
	return OK, path
}

// rootCheck verifies the fjord data root accepts writes -- stacks, catalog
// cache, and provisioned volumes all live under it.
func rootCheck(dir string) Check {
	return Check{
		ID:   "root",
		Name: "fjord data root",
		Probe: func(context.Context) (Status, string) {
			// It's fjord's own directory and fjordd runs as root: create it
			// rather than ask. Startup does the same; this covers a root that
			// vanished while running. Only a failure to create/write is news.
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return Fail, fmt.Sprintf("%s does not exist and could not be created: %v", dir, err)
				}
			}
			f, err := os.CreateTemp(dir, ".doctor-*")
			if err != nil {
				return Fail, fmt.Sprintf("%s exists but is not writable by fjordd: %v", dir, err)
			}
			f.Close()
			os.Remove(f.Name())
			return OK, dir
		},
		Why: "Where fjord keeps everything it manages: stacks, the catalog cache and provisioned app data. fjordd creates it itself; this only fails if the location can't be created or written.",
		Fix: "# check the parent is writable and nothing sits in the way, then\nmkdir -p " + dir + "\nchmod 755 " + dir,
	}
}
