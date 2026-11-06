package doctor

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
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

// processStarted is when this fjordd process started, used to detect a binary
// replaced on disk underneath the running daemon.
var processStarted = time.Now()

// fjorddOutdatedStatus compares the running process's start time with the
// newer of the binary's mtime on disk and its pkg install time (`pkg query %t`).
// A 2-second grace avoids false positives when a binary is started within the
// same second it was written.
func fjorddOutdatedStatus(started, updated time.Time) (Status, string) {
	if updated.IsZero() || !updated.After(started.Add(2*time.Second)) {
		return OK, "running the installed binary"
	}
	return Warn, fmt.Sprintf(
		"running since %s, but the binary on disk was updated %s -- restart fjordd to run the new version",
		started.Format("Jan 2 15:04"), updated.Format("Jan 2 15:04"))
}

// fjorddUpdatedTime returns the newer of the running executable's mtime on disk
// and the `fjord` package install time from `pkg query %t` (since `pkg upgrade`
// preserves the package build mtime on the binary, which can predate the
// running process even when the package was installed after it started).
func fjorddUpdatedTime(ctx context.Context) time.Time {
	var newest time.Time
	if exe, err := os.Executable(); err == nil {
		exe = strings.TrimSuffix(exe, " (deleted)")
		if fi, err := os.Stat(exe); err == nil {
			newest = fi.ModTime()
		}
	}
	if out, err := exec.CommandContext(ctx, "pkg", "query", "%t", "fjord").Output(); err == nil {
		line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		if secs, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64); err == nil {
			if t := time.Unix(secs, 0); t.After(newest) {
				newest = t
			}
		}
	}
	return newest
}

// fjorddOutdatedCheck reports when the fjordd binary or package on disk is
// newer than the running process (e.g. after a pkg upgrade or manual install).
func fjorddOutdatedCheck(fix string) Check {
	return Check{
		ID:       "fjordd-outdated",
		Name:     "fjordd daemon is current",
		HostOnly: true,
		Probe: func(ctx context.Context) (Status, string) {
			return fjorddOutdatedStatus(processStarted, fjorddUpdatedTime(ctx))
		},
		Why: "An upgrade replaced the fjordd binary on disk while the old process kept running. Restarting fjordd switches to the new version without touching running containers.",
		Fix: fix,
	}
}

const (
	storageWarnBytes = 5 << 30 // 5 GB
	storageFailBytes = 1 << 30 // 1 GB
)

// storageLowStatus judges available disk space on the storage root.
func storageLowStatus(path string, avail uint64) (Status, string) {
	gb := float64(avail) / (1 << 30)
	if avail < storageFailBytes {
		return Fail, fmt.Sprintf("%.1f GB free on %s -- pulls and updates will fail on a full disk", gb, path)
	}
	if avail < storageWarnBytes {
		return Warn, fmt.Sprintf("%.1f GB free on %s -- running low on disk space", gb, path)
	}
	return OK, fmt.Sprintf("%.1f GB free on %s", gb, path)
}

// parseGraphRoot extracts `graphroot = "..."` from a containers/storage.conf body.
func parseGraphRoot(conf string) string {
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "graphroot" {
			continue
		}
		v = strings.TrimSpace(v)
		if i := strings.IndexByte(v, '#'); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		v = strings.Trim(v, `"'`)
		if v != "" {
			return v
		}
	}
	return ""
}

// containerStorageRoot finds where OCI images and container layers live on disk.
func containerStorageRoot() string {
	for _, p := range []string{"/usr/local/etc/containers/storage.conf", "/etc/containers/storage.conf"} {
		if b, err := os.ReadFile(p); err == nil {
			if root := parseGraphRoot(string(b)); root != "" {
				if _, err := os.Stat(root); err == nil {
					return root
				}
			}
		}
	}
	for _, p := range []string{"/var/db/containers/storage", "/var/lib/containers/storage"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// storageLowCheck reports when free disk space on the container image store or
// fjord data root falls below 5 GB (warn) or 1 GB (fail).
func storageLowCheck(dir string) Check {
	return Check{
		ID:   "storage-low",
		Name: "disk space on storage root",
		Probe: func(context.Context) (Status, string) {
			var worstPath string
			var worstAvail uint64
			for _, p := range []string{containerStorageRoot(), dir} {
				if p == "" {
					continue
				}
				avail, err := freeBytes(p)
				if err != nil {
					continue
				}
				if worstPath == "" || avail < worstAvail {
					worstPath, worstAvail = p, avail
				}
			}
			if worstPath == "" {
				return OK, ""
			}
			return storageLowStatus(worstPath, worstAvail)
		},
		Why: "Image pulls, container layers and app data share the storage pool. When free space runs out, updates fail mid-pull and databases cannot write.",
		Fix: "fjord prune\n# or see System -> Storage cleanup to reclaim unused images and containers",
	}
}

// hostnameTakenStatus compares the addresses a hostname resolves to in DNS
// against the host's own interface addresses. Loopback and public (non-private)
// answers are ignored so a VPS behind NAT whose name resolves to its public IP
// does not misfire; if every resolved LAN IP belongs to another machine, the
// name is taken.
func hostnameTakenStatus(fqdn string, resolved, own []net.IP) (Status, string) {
	if fqdn == "" || len(resolved) == 0 || len(own) == 0 {
		return OK, ""
	}
	ownSet := make(map[string]bool, len(own))
	for _, ip := range own {
		if u := ip.To4(); u != nil {
			ownSet[u.String()] = true
		} else {
			ownSet[ip.String()] = true
		}
	}
	var foreign []string
	var checkable int
	for _, ip := range resolved {
		if ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		if !ip.IsPrivate() && !ip.IsLinkLocalUnicast() {
			continue
		}
		checkable++
		key := ip.String()
		if u := ip.To4(); u != nil {
			key = u.String()
		}
		if ownSet[key] {
			return OK, fmt.Sprintf("%s resolves to %s", fqdn, key)
		}
		foreign = append(foreign, key)
	}
	if checkable == 0 || len(foreign) == 0 {
		return OK, ""
	}
	return Warn, fmt.Sprintf("%s resolves to %s, which is not an address on this host", fqdn, strings.Join(foreign, ", "))
}

// hostnameTakenCheck checks whether <hostname>.lan (or the first search domain
// in /etc/resolv.conf) resolves in DNS to an address this host does not hold.
func hostnameTakenCheck() Check {
	return Check{
		ID:       "hostname-taken",
		Name:     "host name resolves to this machine",
		Group:    "networking",
		HostOnly: true,
		Probe: func(ctx context.Context) (Status, string) {
			host, err := os.Hostname()
			if err != nil || host == "" {
				return OK, ""
			}
			short, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(host)), ".")
			if short == "" || short == "localhost" {
				return OK, ""
			}
			domain := "lan"
			if b, err := os.ReadFile("/etc/resolv.conf"); err == nil {
				for _, line := range strings.Split(string(b), "\n") {
					f := strings.Fields(line)
					if len(f) > 1 && (f[0] == "search" || f[0] == "domain") {
						d := strings.TrimSuffix(f[1], ".")
						if d != "" && d != "local" {
							domain = d
							break
						}
					}
				}
			}
			fqdn := short + "." + domain
			lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			ips, err := net.DefaultResolver.LookupIP(lctx, "ip4", fqdn)
			if err != nil || len(ips) == 0 {
				return OK, ""
			}
			addrs, err := net.InterfaceAddrs()
			if err != nil {
				return OK, ""
			}
			var own []net.IP
			for _, a := range addrs {
				if ipnet, ok := a.(*net.IPNet); ok {
					own = append(own, ipnet.IP)
				}
			}
			return hostnameTakenStatus(fqdn, ips, own)
		},
		Why: "When your router or DNS server maps this host's name to another address (such as an old DHCP lease from a network probe), links using the host's name fail or reach the wrong machine.",
		Fix: "# Update the host override or static DHCP lease for this host on your router/DNS server\n# so that its name points to this machine's LAN IP address.",
	}
}
