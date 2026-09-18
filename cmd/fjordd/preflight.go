package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/doctor"
	"github.com/daemonless/fjord/pkg/stack"
)

// preflight checks a stack can plausibly come up BEFORE handing it to the
// compose engine, whose own failures are notoriously cryptic ("no such file or
// directory" for a taken port). Returns human-readable problems; empty = go.
//
// Two layers of port checking:
//  1. ports published by other CONTAINERS (via the engine, names the culprit);
//  2. ports held by host PROCESSES, via a TCP dial probe against
//     FJORD_HOST_ADDR (default 127.0.0.1 -- right for host-network deploys;
//     set it to the host's LAN IP when fjordd runs on its own macvlan IP).
//
// The stack's own already-held ports are skipped so re-upping a running stack
// never self-conflicts.
func (s *server) preflight(ctx context.Context, st *stack.Stack) []string {
	env := st.EnvMap()

	problems := s.provisionBindDirs(st, env)

	ports := composepkg.PublishedPorts(st.Compose, env)
	// A stack with an address of its own binds nothing on the host: its ports
	// live on that address. Checking them against the host's would refuse two
	// stacks that each have their own IP and happen to share a port -- which
	// is the whole point of giving them one.
	if net, _ := composepkg.AttachedNetwork(st.Compose); net != "" {
		ports = nil
	}
	hostPorts := s.hostNetworkPorts(ctx, st, env)
	if len(ports) == 0 && len(hostPorts) == 0 {
		return problems
	}

	// Ports this stack's own containers currently hold. A running host-network
	// container holds everything its image exposes, so re-upping never trips
	// on its own postgres.
	own := map[string]bool{}
	running := map[string]bool{}
	if status, err := s.backendFor(st).Status(ctx, st); err == nil {
		for _, c := range status.Containers {
			running[c.Name] = c.State == "running"
			for _, p := range c.Ports {
				proto := p.Protocol
				if proto == "" {
					proto = "tcp"
				}
				own[fmt.Sprintf("%d/%s", p.HostPort, proto)] = true
			}
		}
	}
	for _, hp := range hostPorts {
		if running[hp.container] {
			own[hp.key] = true
		}
	}

	used, _ := s.primaryBackend().UsedPorts(ctx) // best-effort; nil on error
	project := strings.ToLower(st.Name) + "_"
	hostAddr := os.Getenv("FJORD_HOST_ADDR")
	if hostAddr == "" {
		hostAddr = "127.0.0.1"
	}

	for _, p := range ports {
		key := fmt.Sprintf("%d/%s", p.Host, p.Proto)
		if own[key] {
			continue
		}
		if holder, ok := used[key]; ok && !strings.HasPrefix(holder, project) {
			problems = append(problems,
				fmt.Sprintf("port %s is already published by container %q -- stop it or change this stack's port", key, holder))
			continue
		}
		if p.Proto == "tcp" && tcpInUse(hostAddr, p.Host) {
			problems = append(problems,
				fmt.Sprintf("port %s is already in use by a process on the host -- free it or change this stack's port", key))
		}
	}
	seen := map[string]bool{}
	for _, hp := range hostPorts {
		if own[hp.key] || seen[hp.key] {
			continue
		}
		seen[hp.key] = true
		if holder, ok := used[hp.key]; ok && !strings.HasPrefix(holder, project) {
			problems = append(problems,
				fmt.Sprintf("port %s (%s, network_mode: host) is already published by container %q", hp.key, hp.service, holder))
			continue
		}
		if hp.proto == "tcp" && tcpInUse(hostAddr, hp.port) {
			problems = append(problems,
				fmt.Sprintf("port %s is already in use on the host, and %q runs on the host network so it can't bind it -- another stack with the same sidecar? stop it or move this stack off network_mode: host", hp.key, hp.service))
		}
	}
	return problems
}

// tcpInUse reports whether something answers on addr:port right now.
func tcpInUse(addr string, port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(addr, strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// hostPort is one port a network_mode: host service will bind on the host,
// taken from its image's EXPOSE since host-network services never list it
// in `ports:` (a postgres sidecar on 5432, say).
type hostPort struct {
	key       string // "5432/tcp"
	port      int
	proto     string
	service   string
	container string // the container name the engine reports for the service
}

// imageExposer is implemented by engines that can read a local image's EXPOSE
// entries; others simply skip this layer of pre-flight.
type imageExposer interface {
	ImageExposedPorts(ctx context.Context, ref string) ([]string, error)
}

// hostNetworkPorts lists the ports every network_mode: host service will
// take, for images already present. Two stacks that both ship a host-network
// postgres collide on 5432 and the second one crash-loops with nothing but a
// generic "Address already in use" in the sidecar's log -- pre-flight is the
// only place that can say so plainly. Best-effort: an image not pulled yet
// contributes nothing.
func (s *server) hostNetworkPorts(ctx context.Context, st *stack.Stack, env map[string]string) []hostPort {
	be, ok := s.backendFor(st).(imageExposer)
	if !ok {
		return nil
	}
	var out []hostPort
	for _, svc := range composepkg.ParseServices(st.Compose, env) {
		if !svc.NetworkHost || svc.Image == "" {
			continue
		}
		keys, err := be.ImageExposedPorts(ctx, svc.Image)
		if err != nil {
			continue
		}
		for _, k := range keys {
			portStr, proto, _ := strings.Cut(k, "/")
			port, err := strconv.Atoi(portStr)
			if err != nil {
				continue
			}
			if proto == "" {
				proto = "tcp"
			}
			out = append(out, hostPort{
				key: fmt.Sprintf("%d/%s", port, proto), port: port, proto: proto,
				service: svc.Name, container: strings.ToLower(st.Name) + "_" + svc.Name + "_1",
			})
		}
	}
	return out
}

// provisionBindDirs auto-creates missing bind-mount source dirs, chowned to
// the stack's PUID/PGID (default 1000:1000) -- so a fresh or hand-edited
// stack never dies on "config dir doesn't exist", and never on a dir the
// runtime created root-owned on its behalf (podman does that, and the app
// then can't write its own cache). On the host any path qualifies; a jailed
// fjordd only sees the fjord root, and creating through a broader host bind
// would risk writing under hidden mountpoints (nullfs doesn't cross nested
// mounts), so in container mode only paths under the managed root are
// touched. Existing dirs are never chowned. Creation FAILURES are returned as
// problems; paths we can't see are left for the runtime to report.
func (s *server) provisionBindDirs(st *stack.Stack, env map[string]string) []string {
	root := s.fjordRoot + string(os.PathSeparator)
	anywhere := doctor.Mode() == "host"
	uid, gid := 1000, 1000
	if n, err := strconv.Atoi(env["PUID"]); err == nil {
		uid = n
	}
	if n, err := strconv.Atoi(env["PGID"]); err == nil {
		gid = n
	}
	var problems []string
	for _, b := range composepkg.BindMounts(st.Compose, env) {
		clean := filepath.Clean(b.Source)
		if !anywhere && !strings.HasPrefix(clean, root) {
			continue // jailed fjordd: outside the managed root is not ours to create
		}
		if _, err := os.Stat(clean); err == nil {
			continue
		}
		if err := os.MkdirAll(clean, 0o755); err != nil {
			problems = append(problems, fmt.Sprintf("bind mount %s: cannot create: %v", clean, err))
			continue
		}
		_ = os.Chown(clean, uid, gid)
		log.Printf("preflight %s: provisioned %s (uid %d)", st.Name, clean, uid)
	}
	return problems
}
