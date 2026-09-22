package podman

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

// Backend implements the engine.Backend interface for Podman.
type Backend struct {
	http *http.Client // libpod REST API over the podman unix socket, see socket.go
	sock string       // socket path, for raw-dialing the exec hijack
}

// NewBackend initializes the Podman execution engine.
func NewBackend() engine.Backend {
	return &Backend{http: newSocketClient(), sock: socketPath()}
}

// Descriptor registers podman as an engine. It's the default runtime and is
// treated as always available where fjord runs (the podman socket's health is
// reported separately by the doctor).
var Descriptor = engine.Descriptor{
	Name:        "podman",
	Description: "OCI containers via podman + podman-compose.",
	Package:     "podman",
	Available: func() (bool, string, string) {
		if _, err := exec.LookPath("podman"); err != nil {
			return false, "podman not found in PATH", ""
		}
		return true, "", ""
	},
	New: func() engine.Backend { return NewBackend() },
}

// Up brings a stack up. Streamed as create-then-start.
func (b *Backend) Up(ctx context.Context, s *stack.Stack) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		b.bringUp(ctx, pw, s, false, nil)
	}()
	return pr, nil
}

// bringUp brings a stack up WITHOUT a pod (`podman-compose --in-pod=false`).
// podman-compose's default pod-per-project needs an infra container, whose
// init is catatonit; on a host without it the pod is created with no infra
// and the member container dies with a bogus "no such file or directory".
// Without a pod each container is standalone, starts cleanly, and catatonit
// is not needed at all. After that we still `podman start` the stack's
// containers as a safety net (a no-op when compose already started them).
// Single-service stacks -- fjord's whole catalog -- don't need a pod's shared
// netns anyway.
// only limits it to those services (empty = the whole stack): only they are
// recreated, force-removed on a refused recreate, and started.
func (b *Backend) bringUp(ctx context.Context, pw *io.PipeWriter, s *stack.Stack, forceRecreate bool, only []string) {
	b.removeOrphanStorage(ctx, pw, s.Name)
	args := upArgs(forceRecreate, only)
	err := b.runStreaming(ctx, pw, s.Dir, "podman-compose", args...)
	// A plain up tolerates a failure here: compose can leave a container in
	// "created" and the explicit `podman start` below recovers it. A recreate
	// cannot -- if the teardown was refused the OLD container is still there,
	// and starting it would report a successful update while running the image
	// the stack already had. That is the failure this whole path exists to stop.
	if err != nil && forceRecreate {
		// The usual reason a teardown is refused is a lingering exec session:
		// podman keeps the record even after the process is gone, `stop`, `rm`
		// and `container cleanup` all refuse ("container state improper"), and
		// libpod has no endpoint to drop it. Only a force-remove clears it.
		// Safe to do here and nowhere else -- an update is replacing these
		// containers anyway.
		fmt.Fprintf(pw, "\n[warn] recreate refused (%v); force-removing the containers being replaced and retrying\n", err)
		b.forceRemoveStackContainers(ctx, pw, s, only)
		err = b.runStreaming(ctx, pw, s.Dir, "podman-compose", args...)
	}
	if err != nil && forceRecreate {
		fmt.Fprintf(pw, "\n[error] recreate failed, the stack still runs its previous image: %v\n", err)
		return
	}
	cs, err := b.listStackContainers(ctx, s.Name)
	if err != nil {
		// Don't dress a dead socket up as "compose made nothing" -- that sends
		// the operator to the compose file when the fix is `service podman
		// restart`.
		fmt.Fprintf(pw, "\n[error] cannot reach podman to list this stack's containers: %v\n", err)
		return
	}
	names := containerNames(ofServices(cs, only))
	if len(names) == 0 {
		fmt.Fprintf(pw, "\n[error] compose created no containers to start\n")
		return
	}
	if err := b.runStreaming(ctx, pw, s.Dir, "podman", append([]string{"start"}, names...)...); err != nil {
		fmt.Fprintf(pw, "\n[error] start: %v\n", err)
	}
}

// forceRemoveStackContainers force-removes this stack's own containers, used
// only to unwedge a refused recreate. Scoped to the stack's containers, so it
// can never touch anything else on the host.
func (b *Backend) forceRemoveStackContainers(ctx context.Context, pw *io.PipeWriter, s *stack.Stack, only []string) {
	cs, err := b.listStackContainers(ctx, s.Name)
	if err != nil {
		fmt.Fprintf(pw, "[warn] cannot reach podman to list this stack's containers: %v\n", err)
		return
	}
	names := containerNames(ofServices(cs, only))
	if len(names) == 0 {
		return
	}
	if err := b.runStreaming(ctx, pw, s.Dir, "podman", append([]string{"rm", "-f"}, names...)...); err != nil {
		fmt.Fprintf(pw, "[warn] force-remove: %v\n", err)
	}
}

// removeOrphanStorage drops "external" storage containers that carry this
// stack's own container names (<stack>_<service>_N). podman doesn't own
// those records -- they are what a storage wedge leaves behind when a stack
// is deleted -- and `compose up` refuses to reuse the name ("already in use
// by an external entity"), which bit a stack that got a deleted stack's id.
// A name in the stack's namespace can't be anything but its own leftover,
// so it is removed (with force: the stale record usually claims to be
// mounted) and the removal is reported in the output.
func (b *Backend) removeOrphanStorage(ctx context.Context, w io.Writer, project string) {
	out, err := exec.CommandContext(ctx, "podman", "ps", "-a", "--external", "--format", "json").Output()
	if err != nil {
		return
	}
	var list []struct {
		ID    string `json:"Id"`
		Names []string
		State string
	}
	if json.Unmarshal(out, &list) != nil {
		return
	}
	prefix := strings.ToLower(project) + "_"
	for _, c := range list {
		if !strings.EqualFold(c.State, "storage") {
			continue
		}
		for _, n := range c.Names {
			if strings.HasPrefix(strings.ToLower(n), prefix) {
				fmt.Fprintf(w, "[fjord] removing orphaned storage container %s (%s) left by an earlier stack\n", n, c.ID[:12])
				if o, err := exec.CommandContext(ctx, "podman", "rm", "--storage", "--force", c.ID).CombinedOutput(); err != nil {
					fmt.Fprintf(w, "[fjord] could not remove %s: %v: %s\n", n, err, strings.TrimSpace(string(o)))
				}
				break
			}
		}
	}
}

// Down tears a stack down. `compose down` can refuse to remove a container
// that has leaked exec sessions ("container state improper" -- interactive
// shells leave these behind on FreeBSD) -- and podman-compose sometimes exits
// 0 despite that. So trust the outcome, not the exit code: any containers
// still present afterwards are force-removed (rm -f ignores exec sessions),
// then the leftover compose network is dropped.
func (b *Backend) Down(ctx context.Context, s *stack.Stack) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		_ = b.runStreaming(ctx, pw, s.Dir, "podman", "compose", "down")
		// Force-remove survivors ONE AT A TIME, in passes: a single multi-arg
		// `rm -f a b c` trips podman's dependency check against stale state
		// ("has dependent containers ... already exists" for a container the
		// same command just removed). Individually, dependents fall in pass 1
		// and free their dependencies for pass 2.
		listFailed := false
		for pass := 0; pass < 3; pass++ {
			cs, err := b.listStackContainers(ctx, s.Name)
			if err != nil {
				fmt.Fprintf(pw, "\n[warn] cannot reach podman to check for surviving containers: %v\n", err)
				listFailed = true
				break
			}
			names := containerNames(cs)
			if len(names) == 0 {
				break
			}
			if pass == 0 {
				fmt.Fprintf(pw, "\n[fjord] containers survived compose down; force-removing\n")
			}
			for _, n := range names {
				_ = b.runStreaming(ctx, pw, s.Dir, "podman", "rm", "-f", n)
			}
		}
		// Only drop the compose network once the stack is CONFIRMED empty. A
		// failed list is not an empty list: removing the network out from
		// under containers that are still up is how a "down" turns into a
		// half-torn-down stack with no connectivity.
		if !listFailed {
			cs, err := b.listStackContainers(ctx, s.Name)
			switch {
			case err != nil:
				fmt.Fprintf(pw, "[warn] cannot confirm the stack is gone (%v); leaving %s_default in place\n",
					err, strings.ToLower(s.Name))
			case len(containerNames(cs)) == 0:
				_ = b.runStreaming(ctx, pw, s.Dir, "podman", "network", "rm", "-f", strings.ToLower(s.Name)+"_default")
			}
		}
		// A storage record podman's rm couldn't finish (wedged storage) would
		// outlive the stack under its name; sweep it now, not at the next Up.
		b.removeOrphanStorage(ctx, pw, s.Name)
	}()
	return pr, nil
}

// Restart does an explicit `compose stop` then `compose start`, NOT
// `compose restart`. The external podman-compose provider's "restart" recreates
// the container, so its new instance tries to bind published host ports before
// the old instance releases them -> "address already in use". Stopping first
// frees the port, then start rebinds it cleanly. Streamed as two steps.
func (b *Backend) Restart(ctx context.Context, s *stack.Stack) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		// stop, then the same create-then-start as Up: `podman compose start`
		// alone can leave a container in "created" on FreeBSD (seen with
		// sonarr), and bringUp's explicit `podman start` catches that.
		if err := b.runStreaming(ctx, pw, s.Dir, "podman", "compose", "stop"); err != nil {
			fmt.Fprintf(pw, "\n[error] stop: %v\n", err)
			return
		}
		b.bringUp(ctx, pw, s, false, nil)
	}()
	return pr, nil
}

// Update pulls the latest images then FORCE-recreates the stack. If pull fails
// it stops before recreating so a bad pull can't tear down a working stack.
//
// The force matters: podman-compose only recreates when the compose file's hash
// changes, so pulling a moved tag left the old container running on the old
// image -- the pull succeeded, the UI said updated, and nothing had changed.
//
// services narrows both steps to those services. --no-deps keeps compose from
// recreating what they depend on, and it leaves what depends on THEM alone
// too: updating immich's database does not restart its server.
func (b *Backend) Update(ctx context.Context, s *stack.Stack, services []string) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		if err := b.runStreaming(ctx, pw, s.Dir, "podman", append([]string{"compose", "pull"}, services...)...); err != nil {
			fmt.Fprintf(pw, "\n[error] pull: %v\n", err)
			return
		}
		b.bringUp(ctx, pw, s, true, services)
	}()
	return pr, nil
}

// Logs streams the stack's container logs, one `podman logs` per container
// merged with a "service |" prefix. NOT `compose logs`: the remote podman
// client (fjordd drives the host over the socket) refuses multiple containers
// ("logs does not support multiple containers when run remotely"). With follow
// it tails until ctx is cancelled (client disconnect kills the processes).
func (b *Backend) Logs(ctx context.Context, s *stack.Stack, tail int, follow bool, containers []string) (io.ReadCloser, error) {
	cs, listErr := b.listStackContainers(ctx, s.Name)
	names := containerNames(cs)
	// Narrow to the requested subset -- membership-checked against the stack's
	// own containers so the endpoint can't read arbitrary logs.
	if len(containers) > 0 {
		want := map[string]bool{}
		for _, c := range containers {
			want[c] = true
		}
		narrowed := names[:0:0]
		for _, n := range names {
			if want[n] {
				narrowed = append(narrowed, n)
			}
		}
		names = narrowed
	}
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		if listErr != nil {
			fmt.Fprintf(pw, "[fjord] cannot reach podman to list this stack's containers: %v\n", listErr)
			return
		}
		if len(names) == 0 {
			fmt.Fprintf(pw, "[fjord] no containers for this stack\n")
			return
		}
		fmt.Fprintf(pw, "$ podman logs --tail %d%s %s\n", tail, map[bool]string{true: " -f"}[follow], strings.Join(names, " "))
		var wg sync.WaitGroup
		var mu sync.Mutex
		project := strings.ToLower(s.Name) + "_"
		for _, name := range names {
			args := []string{"logs", "--tail", strconv.Itoa(tail)}
			if follow {
				args = append(args, "-f")
			}
			args = append(args, name)
			// Short service label: <project>_<service>_1 -> <service>.
			label := strings.TrimPrefix(name, project)
			if i := strings.LastIndex(label, "_"); i > 0 {
				label = label[:i]
			}
			if len(names) == 1 {
				label = "" // single container: no prefix noise
			}
			wg.Add(1)
			go func(label string, args []string) {
				defer wg.Done()
				cmd := exec.CommandContext(ctx, "podman", args...)
				out, err := cmd.StdoutPipe()
				if err != nil {
					return
				}
				cmd.Stderr = cmd.Stdout
				if cmd.Start() != nil {
					return
				}
				// bufio.Reader, not Scanner: a Scanner stops at its buffer
				// limit (a >1MB minified line) and leaves the pipe unread,
				// so Wait() below never returns.
				rd := bufio.NewReaderSize(out, 64*1024)
				for {
					line, err := rd.ReadString('\n')
					if line != "" {
						mu.Lock()
						if label != "" {
							fmt.Fprintf(pw, "%s | %s", label, line)
						} else {
							fmt.Fprint(pw, line)
						}
						if !strings.HasSuffix(line, "\n") {
							fmt.Fprint(pw, "\n")
						}
						mu.Unlock()
					}
					if err != nil {
						break
					}
				}
				cmd.Wait()
			}(label, args)
		}
		wg.Wait()
	}()
	return pr, nil
}

// runStreaming runs a command with stdout+stderr merged into w, blocking until
// it exits, after announcing the command as a "$ ..." header line. The backend
// -- not the UI -- is the source of truth for what's actually being run, so a
// different runtime's backend shows its own commands without UI changes.
func (b *Backend) runStreaming(ctx context.Context, w io.Writer, dir, name string, args ...string) error {
	fmt.Fprintf(w, "$ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// ofServices narrows a stack's containers to those services, by podman-compose's
// service label. An empty list is the whole stack.
func ofServices(cs []libpodContainer, services []string) []libpodContainer {
	if len(services) == 0 {
		return cs
	}
	var out []libpodContainer
	for _, c := range cs {
		if slices.Contains(services, c.Labels["io.podman.compose.service"]) {
			out = append(out, c)
		}
	}
	return out
}

// upArgs is the podman-compose command line for bringUp.
func upArgs(forceRecreate bool, only []string) []string {
	args := []string{"--in-pod=false", "up", "-d"}
	// Never with a service list: podman-compose 1.5 then counts every service
	// NOT named as an orphan and deletes its container. Updating immich's
	// database would have removed its server, ML and redis. A whole-stack up
	// still clears services dropped from the compose.
	if len(only) == 0 {
		args = append(args, "--remove-orphans")
	}
	if forceRecreate {
		// podman-compose decides whether to recreate by comparing a hash of the
		// compose FILE, not the image: `up -d` after a pull leaves the existing
		// container in place and `podman start` then starts it on the old image.
		// An update that changes the tag edits the compose and so recreates by
		// itself; one that pulls a moved tag (:latest) does not, and silently
		// keeps running the image it already had.
		args = append(args, "--force-recreate")
	}
	if len(only) > 0 {
		args = append(append(args, "--no-deps"), only...)
	}
	return args
}
