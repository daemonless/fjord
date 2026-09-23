package podman

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
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
	b.releaseOrphanAddresses(ctx, pw, s)
	args := upArgs(forceRecreate, only)
	started := time.Now()
	err := b.runStreaming(ctx, pw, s.Dir, "podman-compose", args...)
	// A plain up tolerates a failure here: compose can leave a container in
	// "created" and the explicit `podman start` below recovers it. A recreate
	// cannot -- if the teardown was refused the OLD container is still there,
	// and starting it would report a successful update while running the image
	// the stack already had. That is the failure this whole path exists to stop.
	//
	// A refusal does not always fail the command: podman-compose prints
	// "active exec sessions ... name already in use", starts the old container
	// again and exits 0. So the retry is decided by the containers, not the
	// exit code -- any that predate this recreate were not replaced.
	if forceRecreate {
		stale := b.notRecreated(ctx, s, only, started)
		if err != nil || len(stale) > 0 {
			// The usual reason a teardown is refused is a lingering exec
			// session: podman keeps the record even after the process is gone,
			// `stop`, `rm` and `container cleanup` all refuse ("container state
			// improper"), and libpod has no endpoint to drop it. Only a
			// force-remove clears it. Safe to do here and nowhere else -- an
			// update is replacing these containers anyway.
			targets, why := only, fmt.Sprint(err)
			if err == nil {
				targets, why = stale, "left "+strings.Join(stale, ", ")+" in place"
			}
			fmt.Fprintf(pw, "\n[warn] recreate refused (%s); force-removing the containers being replaced and retrying\n", why)
			b.forceRemoveStackContainers(ctx, pw, s, targets)
			b.releaseOrphanAddresses(ctx, pw, s)
			err = b.runStreaming(ctx, pw, s.Dir, "podman-compose", args...)
		}
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
//
// Only the named services are pulled. What depends on them is recreated with
// them (see composepkg.WithDependents) but keeps the image it has -- updating
// the database must not quietly update the server too.
func (b *Backend) Update(ctx context.Context, s *stack.Stack, services []string) (io.ReadCloser, error) {
	recreate := services
	if len(services) > 0 {
		recreate = composepkg.WithDependents(s.Compose, services)
	}
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		if more := extra(recreate, services); len(more) > 0 {
			fmt.Fprintf(pw, "[fjord] also recreating %s: it depends on %s\n", strings.Join(more, ", "), strings.Join(services, ", "))
		}
		if err := b.runStreaming(ctx, pw, s.Dir, "podman", append([]string{"compose", "pull"}, services...)...); err != nil {
			fmt.Fprintf(pw, "\n[error] pull: %v\n", err)
			return
		}
		started := time.Now()
		b.bringUp(ctx, pw, s, true, recreate)
		if b.verifyRecreated(ctx, pw, s, recreate, started) {
			b.watchHealthy(ctx, pw, s, recreate, healthWindow)
		}
	}()
	return pr, nil
}

// extra is what all has that some does not.
func extra(all, some []string) []string {
	var out []string
	for _, a := range all {
		if !slices.Contains(some, a) {
			out = append(out, a)
		}
	}
	return out
}

// notRecreated lists the services (all of them when services is empty) whose
// container is older than since -- ones a recreate did not replace.
func (b *Backend) notRecreated(ctx context.Context, s *stack.Stack, services []string, since time.Time) []string {
	cs, err := b.listStackContainers(ctx, s.Name)
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range ofServices(cs, services) {
		svc := c.Labels["io.podman.compose.service"]
		// A second of slack: podman's timestamp and ours round differently.
		if svc != "" && c.Created.Before(since.Add(-time.Second)) && !slices.Contains(out, svc) {
			out = append(out, svc)
		}
	}
	return out
}

// verifyRecreated checks that every service an update meant to replace now
// runs a container created after the update began.
//
// podman-compose exits 0 when a recreate fails: it prints "has dependent
// containers" and "name already in use", starts the OLD container again, and
// reports success. The exit code cannot say whether an update happened, so
// the containers are asked instead. services empty = the whole stack.
func (b *Backend) verifyRecreated(ctx context.Context, pw *io.PipeWriter, s *stack.Stack, services []string, since time.Time) bool {
	cs, err := b.listStackContainers(ctx, s.Name)
	if err != nil {
		fmt.Fprintf(pw, "\n[error] cannot confirm the update: %v\n", err)
		return false
	}
	ok := true
	if len(services) == 0 {
		for _, c := range cs {
			if svc := c.Labels["io.podman.compose.service"]; svc != "" && !slices.Contains(services, svc) {
				services = append(services, svc)
			}
		}
	}
	for _, svc := range services {
		found := false
		for _, c := range ofServices(cs, []string{svc}) {
			found = true
			// A second of slack: podman's timestamp and ours come from
			// different clocks' roundings, never from different hosts.
			if c.Created.Before(since.Add(-time.Second)) {
				fmt.Fprintf(pw, "\n[error] %s was not updated: it still runs the container from %s, on the image it had\n",
					svc, c.Created.Local().Format("Jan 2 15:04"))
				ok = false
			}
		}
		if !found {
			fmt.Fprintf(pw, "\n[error] %s has no container after the update\n", svc)
			ok = false
		}
	}
	return ok
}

// healthWindow is how long an updated container must stay up to count as
// working. Long enough for an app that dies on a bad config or a failed
// migration to show it; short enough to wait through in the Output pane.
var healthWindow = 30 * time.Second

// containerHealth is the part of a container's inspect the health watch reads.
type containerHealth struct {
	RestartCount int `json:"RestartCount"`
	State        struct {
		Running   bool      `json:"Running"`
		ExitCode  int       `json:"ExitCode"`
		StartedAt time.Time `json:"StartedAt"`
		Health    *struct {
			Status string `json:"Status"`
		} `json:"Health"` // absent without a HEALTHCHECK -- true of most images
	} `json:"State"`
}

func (b *Backend) inspectHealth(ctx context.Context, id string) (containerHealth, error) {
	var h containerHealth
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/containers/"+url.PathEscape(id)+"/json", nil)
	if err != nil {
		return h, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return h, fmt.Errorf("inspect %s: %s", id, resp.Status)
	}
	return h, json.NewDecoder(resp.Body).Decode(&h)
}

// watchHealthy says whether updated containers stay up.
//
// "The update ran" is not "the app works": a new image that dies on its
// config, or on a migration, still recreates cleanly, and with restart:
// always it even reads "running" between crashes. So each recreated
// container is watched for a window: it fails if it stops, if it restarts
// (RestartCount or StartedAt moves), or if a HEALTHCHECK says unhealthy. The
// failure is an [error] line, which the UI reads as a failed update.
func (b *Backend) watchHealthy(ctx context.Context, pw *io.PipeWriter, s *stack.Stack, services []string, window time.Duration) {
	cs, err := b.listStackContainers(ctx, s.Name)
	if err != nil {
		fmt.Fprintf(pw, "\n[error] cannot watch the updated containers: %v\n", err)
		return
	}
	type watched struct {
		svc, id string
		first   containerHealth
		failed  bool
	}
	var ws []*watched
	for _, c := range ofServices(cs, services) {
		h, err := b.inspectHealth(ctx, c.ID)
		if err != nil {
			continue
		}
		ws = append(ws, &watched{svc: c.Labels["io.podman.compose.service"], id: c.ID, first: h})
	}
	if len(ws) == 0 {
		return
	}
	names := make([]string, len(ws))
	for i, w := range ws {
		names[i] = w.svc
	}
	fmt.Fprintf(pw, "\n[fjord] checking %s stay%s up for %s\n", strings.Join(names, ", "),
		map[bool]string{true: "s", false: ""}[len(ws) == 1], window)

	deadline := time.Now().Add(window)
	for {
		live := 0
		for _, w := range ws {
			if w.failed {
				continue
			}
			h, err := b.inspectHealth(ctx, w.id)
			switch {
			case err != nil:
				fmt.Fprintf(pw, "[error] %s is gone since the update: %v\n", w.svc, err)
				w.failed = true
			case !h.State.Running:
				fmt.Fprintf(pw, "[error] %s stopped after the update (exit code %d) -- see its logs\n", w.svc, h.State.ExitCode)
				w.failed = true
			case h.RestartCount > w.first.RestartCount || !h.State.StartedAt.Equal(w.first.State.StartedAt):
				fmt.Fprintf(pw, "[error] %s restarted since the update -- it is crash-looping; see its logs\n", w.svc)
				w.failed = true
			case h.State.Health != nil && h.State.Health.Status == "unhealthy":
				fmt.Fprintf(pw, "[error] %s reports unhealthy since the update\n", w.svc)
				w.failed = true
			default:
				live++
			}
		}
		if live == 0 || !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	for _, w := range ws {
		if !w.failed {
			fmt.Fprintf(pw, "[fjord] %s has stayed up for %s\n", w.svc, window)
		}
	}
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

// releaseOrphanAddresses frees addresses on the stack's networks still held
// by containers that no longer exist (see hostnet.ReleaseOrphans). Without
// the full list of containers nothing is freed: an unreadable list would make
// every reservation look orphaned.
func (b *Backend) releaseOrphanAddresses(ctx context.Context, pw io.Writer, s *stack.Stack) {
	atts := composepkg.AttachedNetworks(s.Compose)
	if len(atts) == 0 {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/containers/json?all=true", nil)
	if err != nil {
		return
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var all []struct {
		ID string `json:"Id"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&all) != nil {
		return
	}
	live := map[string]bool{}
	for _, c := range all {
		live[c.ID] = true
	}
	isLive := func(id string) bool { return live[id] }
	// Addresses this stack pins first, with no age guard: they are its own,
	// and on a cni-epair that never releases they are still held by the
	// container a recreate just removed.
	for _, byNet := range composepkg.ServiceAttachments(s.Compose) {
		for _, a := range byNet {
			if a.IP != "" && hostnet.ReleaseAddress(a.Network, a.IP, isLive) {
				fmt.Fprintf(pw, "[fjord] released %s on %s for its own service: held by a removed container\n", a.IP, a.Network)
			}
		}
	}
	seen := map[string]bool{}
	for _, a := range atts {
		if seen[a.Network] {
			continue
		}
		seen[a.Network] = true
		for _, addr := range hostnet.ReleaseOrphans(a.Network, isLive, 2*time.Minute) {
			fmt.Fprintf(pw, "[fjord] released %s on %s: held by a container that no longer exists\n", addr, a.Network)
		}
	}
}
