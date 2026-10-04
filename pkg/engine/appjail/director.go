package appjail

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/stack"
	"gopkg.in/yaml.v3"
)

// directorFile returns the path to a stack's appjail-director.yml when the
// install materialized one, else "". Its presence is the per-stack switch onto
// the appjail-director path (multi-jail orchestration from a dbuild bundle);
// stacks without it stay on the legacy per-service `appjail oci run` path, so
// existing stacks are untouched.
func directorFile(s *stack.Stack) string {
	if s.Dir == "" {
		return ""
	}
	p := filepath.Join(s.Dir, "appjail-director.yml")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// directorLocks serializes director runs per stack. Director takes its own
// project lock and a second concurrent run (Apply while a restart is still
// going) fails with ProjectLocked / exit 70 instead of waiting; queue them here.
var directorLocks sync.Map

func directorLock(dir string) *sync.Mutex {
	mu, _ := directorLocks.LoadOrStore(dir, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// runDirector runs one `appjail-director <args>` from the stack dir (so director
// finds appjail-director.yml, Makejail and .env -- which it auto-loads from the
// cwd, picking up DIRECTOR_PROJECT and every ${VAR} the bundle references).
// fjordd runs as root, so director's per-project state stays under one home for
// the life of the stack (up and down must run as the same user or director
// reports the project "not found"). Streams the command's output and returns
// its error. tolerateMissing turns a "Project not found" exit into success --
// tearing down an already-stopped stack is a normal delete, not an error, so
// it is reported as "nothing to tear down" rather than a raw non-zero exit.
func runDirector(ctx context.Context, w io.Writer, dir string, tolerateMissing bool, args ...string) error {
	cmd := exec.CommandContext(ctx, "appjail-director", args...)
	cmd.Dir = dir
	cmd.Env = directorEnv(dir)
	// Stream live -- `up` can pull and build for a while, and an empty Output
	// panel until it finishes reads as "nothing happened". A copy is kept only
	// to recognise the tolerated "Project not found" exit afterwards.
	var buf bytes.Buffer
	fmt.Fprintf(w, "$ appjail-director %s\n", strings.Join(args, " "))
	// One writer for both, so os/exec serializes the writes into buf.
	out := io.MultiWriter(w, &buf)
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	if err != nil && tolerateMissing && bytes.Contains(buf.Bytes(), []byte("Project not found")) {
		fmt.Fprintln(w, "[fjord] nothing to tear down (already stopped)")
		return nil
	}
	if err != nil {
		// [error], not [fjord]: the UI reads an action as failed only from an
		// [error] line, so this used to report a failed `up` as a success.
		fmt.Fprintf(w, "[error] appjail-director %s failed: %v\n", args[0], err)
		showDirectorLog(ctx, w, dir)
	}
	return err
}

// ansiColor matches the colour codes appjail writes into its logs.
var ansiColor = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// showDirectorLog prints the end of each log from the project's last run.
//
// Director writes why a service failed to its own log directory, not to
// stdout: on netlab `up` failed with nothing in the Output panel but a
// stream of "Creating ..." lines, and the reason -- "git(1) is not
// installed", from building a gh+ makejail -- was only in
// /root/.director/logs/<run>/<service>/makejail.log.
func showDirectorLog(ctx context.Context, w io.Writer, dir string) {
	cmd := exec.CommandContext(ctx, "appjail-director", "info")
	cmd.Dir = dir
	cmd.Env = directorEnv(dir)
	out, _ := cmd.CombinedOutput()
	logDir := ""
	for _, line := range strings.Split(string(out), "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "last log:") {
			logDir = strings.TrimSpace(strings.TrimPrefix(l, "last log:"))
		}
	}
	if logDir == "" {
		return
	}
	_ = filepath.WalkDir(logDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lines := strings.Split(strings.TrimRight(ansiColor.ReplaceAllString(string(data), ""), "\n"), "\n")
		if len(lines) > 12 {
			lines = lines[len(lines)-12:]
		}
		fmt.Fprintf(w, "\n[fjord] %s:\n", path)
		for _, l := range lines {
			fmt.Fprintf(w, "  %s\n", l)
		}
		return nil
	})
}

// directorEnv is the environment director runs with: the daemon's, with HOME
// and PWD pinned so a daemon run behaves like an operator's shell in the stack
// dir. Under rc(8) fjordd starts with HOME=/ and no PWD: director kept its
// project state in /.director (invisible to a root shell, whose HOME is
// /root, which then reported the project "not found"), and the bundle's
// `template: !ENV '${PWD}/template.conf'` expanded to "//template.conf" on
// every rebuild. exec sets the cwd but never PWD -- that is a shell habit.
//
// DIRECTOR_PROJECT is the stack's name unless its .env says otherwise. A
// stack written in the editor has no .env, and director then makes up a new
// project name on every run: each `up` rebuilt every jail as a new project,
// and `down` found no project, so deleting the stack left its jails running.
func directorEnv(dir string) []string {
	out := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "PWD=") && !strings.HasPrefix(kv, "DIRECTOR_PROJECT=") {
			out = append(out, kv)
		}
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		out = append(out, "HOME="+u.HomeDir)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, ".env")); (&stack.Stack{Env: string(raw)}).EnvMap()["DIRECTOR_PROJECT"] == "" {
		out = append(out, "DIRECTOR_PROJECT="+filepath.Base(dir))
	}
	return append(out, "PWD="+dir)
}

// directorOp streams a sequence of director runs under the stack's lock. A
// failed step stops the sequence (no `up` after a failed `down`).
func directorOp(ctx context.Context, s *stack.Stack, steps ...func(io.Writer) error) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		mu := directorLock(s.Dir)
		mu.Lock()
		defer mu.Unlock()
		for _, step := range steps {
			if err := step(pw); err != nil {
				return
			}
		}
	}()
	return pr, nil
}

// directorUp brings the whole project up (director builds each jail from its
// Makejail, applies the oci: config, and starts it in dependency order). It is
// idempotent: an unchanged, already-running project is "Nothing to do.", so
// start-on-boot re-running Up never churns a running stack.
func directorUp(ctx context.Context, s *stack.Stack) (io.ReadCloser, error) {
	return directorOp(ctx, s, func(w io.Writer) error { return runDirector(ctx, w, s.Dir, false, "up") })
}

// directorDown tears the project down. `--destroy` removes the jails (not just
// stops them), matching podman-compose `down` and the legacy oci-run Down, so
// stackDelete never orphans a stopped-but-present jail. Up rebuilds from the
// Makejail. A missing project is tolerated (delete of an already-stopped stack).
func directorDown(ctx context.Context, s *stack.Stack) (io.ReadCloser, error) {
	return directorOp(ctx, s, func(w io.Writer) error { return downDestroy(ctx, stackDir(s.Dir), w) })
}

// directorRestart stops then starts in place. Plain `down` (no --destroy) keeps
// the built jails, so `up` restarts them without re-pulling the image. The down
// tolerates a missing project so restarting a stopped stack just brings it up.
func directorRestart(ctx context.Context, s *stack.Stack) (io.ReadCloser, error) {
	return directorOp(ctx, s,
		func(w io.Writer) error { return runDirector(ctx, w, s.Dir, true, "down") },
		func(w io.Writer) error { return runDirector(ctx, w, s.Dir, false, "up") })
}

// directorUpdate recreates the project with a fresh image pull: `down
// --destroy` drops the jails, then `up` rebuilds from the Makejail, whose
// director spec passes `--pull` to buildah.
func directorUpdate(ctx context.Context, s *stack.Stack, jails []svcJail) (io.ReadCloser, error) {
	return directorOp(ctx, s,
		func(w io.Writer) error { return pullNamedImages(ctx, w, jails) },
		func(w io.Writer) error { return downDestroy(ctx, stackDir(s.Dir), w) },
		func(w io.Writer) error { return runDirector(ctx, w, s.Dir, false, "up") })
}

// directorUpdateServices rebuilds only the given services' jails.
//
// Director has no "rebuild this one" flag, but `up` creates any service whose
// jail is missing and leaves a running, unchanged one alone (proven on netlab
// 2026-10-04: the untouched jail kept its jid). So the jails are destroyed
// here and `up` puts them back, pulling as it builds. Nothing is touched in
// the Makejail to force it: services that name no makejail: of their own all
// share that file, and director rebuilds every one of them when it is newer.
func directorUpdateServices(ctx context.Context, s *stack.Stack, jails []svcJail) (io.ReadCloser, error) {
	return directorOp(ctx, s,
		func(w io.Writer) error { return pullNamedImages(ctx, w, jails) },
		func(w io.Writer) error {
			for _, sj := range jails {
				if err := destroyJail(ctx, w, sj.jail); err != nil {
					fmt.Fprintf(w, "[error] %s: could not remove its jail %s: %v\n", sj.svc.Name, sj.jail, err)
					return err
				}
			}
			return nil
		},
		func(w io.Writer) error { return runDirector(ctx, w, s.Dir, false, "up") },
		func(w io.Writer) error {
			// Director exits 0 on "Nothing to do." as well; ask the jails.
			var down []string
			for _, sj := range jails {
				if exec.CommandContext(ctx, "appjail", "status", "-q", sj.jail).Run() != nil {
					down = append(down, sj.svc.Name)
				}
			}
			if len(down) > 0 {
				fmt.Fprintf(w, "[error] not running after the update: %s\n", strings.Join(down, ", "))
				return fmt.Errorf("not running: %s", strings.Join(down, ", "))
			}
			return nil
		})
}

// pullNamedImages pulls the image of each service that names one (`from:`).
//
// A service built from the Makejail is pulled by buildah as it builds: the
// bundle gives it `container: args:--pull`. One that names its own image --
// every sidecar -- has no such option, so a rebuild reused whatever the host
// already had and an update changed nothing. Pulled before anything is
// destroyed: a pull that fails leaves the stack as it runs.
func pullNamedImages(ctx context.Context, w io.Writer, jails []svcJail) error {
	seen := map[string]bool{}
	for _, sj := range jails {
		if sj.from == "" || seen[sj.from] || strings.Contains(sj.from, "@") {
			continue // a digest names one image for good: nothing newer to fetch
		}
		seen[sj.from] = true
		fmt.Fprintf(w, "$ buildah pull --policy always %s\n", sj.from)
		// --policy always: the default is "missing", which is satisfied by the
		// stale copy this is here to replace.
		cmd := exec.CommandContext(ctx, "buildah", "pull", "--policy", "always", sj.from)
		cmd.Stdout, cmd.Stderr = w, w
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(w, "[error] pull %s (%s): %v -- nothing was changed\n", sj.from, sj.svc.Name, err)
			return err
		}
	}
	return nil
}

// jailsOf picks the named services out of a stack's, in the order asked.
func jailsOf(all []svcJail, services []string) ([]svcJail, error) {
	var out []svcJail
	for _, want := range services {
		found := false
		for _, sj := range all {
			if sj.svc.Name == want {
				out, found = append(out, sj), true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("no service %q in its appjail-director.yml", want)
		}
	}
	return out, nil
}

// destroyJail stops and removes one jail, as director's own destroy does. A
// jail that does not exist is fine: `up` creates it either way.
func destroyJail(ctx context.Context, w io.Writer, jail string) error {
	if exec.CommandContext(ctx, "appjail", "jail", "get", "--", jail, "name").Run() != nil {
		return nil
	}
	// Stopped before it is destroyed: destroying an OCI jail that is up
	// leaves its buildah layer flagged incomplete, and every podman and
	// buildah command on the host fails until it is repaired.
	if exec.CommandContext(ctx, "appjail", "status", "-q", jail).Run() == nil {
		if err := runAppjail(ctx, w, "stop", "--", jail); err != nil {
			return err
		}
	}
	var buf strings.Builder
	err := runAppjail(ctx, io.MultiWriter(w, &buf), "jail", "destroy", "-f", "--", jail)
	if err == nil || !containsBusyMount(buf.String()) {
		return err
	}
	cleared, ferr := forceUnmountJail(ctx, jail)
	if ferr != nil {
		fmt.Fprintf(w, "[fjord] %v\n", ferr)
	}
	if len(cleared) == 0 {
		return err
	}
	fmt.Fprintf(w, "[fjord] a volume was still mounted and would not come off; forced it and retrying: %s\n",
		strings.Join(cleared, ", "))
	return runAppjail(ctx, w, "jail", "destroy", "-f", "--", jail)
}

// runAppjail runs one `appjail <args>`, streaming its output without the
// colour codes appjail writes.
func runAppjail(ctx context.Context, w io.Writer, args ...string) error {
	fmt.Fprintf(w, "$ appjail %s\n", strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, "appjail", args...).CombinedOutput()
	w.Write(ansiColor.ReplaceAll(out, nil))
	return err
}

// svcJail pairs a service (name + published ports, all Status/Logs need) with
// the jail that runs it.
type svcJail struct {
	svc  composepkg.Service
	jail string
	// from is the image the service names itself (`from:`), "" when it takes
	// the Makejail's.
	from string
}

// serviceJails lists a stack's services and their jails from its director
// spec: each service's `name:` IS its jail (whatever the user renamed it to
// in the editor), and ports come from its `expose:` options. A stack without
// a spec has no services to report.
func (b *Backend) serviceJails(s *stack.Stack) []svcJail {
	p := directorFile(s)
	if p == "" {
		return nil
	}
	return directorServices(p, s)
}

// directorServices parses appjail-director.yml into services + jails. expose
// values are `!ENV '${WEB_PORT}:6767 proto:tcp'` -- resolved against the
// stack's .env (read from disk when the in-memory stack lacks it).
func directorServices(path string, s *stack.Stack) []svcJail {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}
	env := s.EnvMap()
	if len(env) == 0 {
		if raw, err := os.ReadFile(filepath.Join(s.Dir, ".env")); err == nil {
			env = (&stack.Stack{Env: string(raw)}).EnvMap()
		}
	}
	services := yamlMapValue(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil
	}
	var out []svcJail
	for i := 0; i+1 < len(services.Content); i += 2 {
		key, body := services.Content[i].Value, services.Content[i+1]
		if body.Kind != yaml.MappingNode {
			continue
		}
		jail := key
		if n := yamlMapValue(body, "name"); n != nil && n.Value != "" {
			jail = n.Value
		}
		svc := composepkg.Service{Name: key}
		from := ""
		if opts := yamlMapValue(body, "options"); opts != nil && opts.Kind == yaml.SequenceNode {
			for _, item := range opts.Content {
				if item.Kind != yaml.MappingNode || len(item.Content) < 2 {
					continue
				}
				if item.Content[0].Value == "from" {
					from = composepkg.ExpandEnv(item.Content[1].Value, env)
				}
				if item.Content[0].Value != "expose" {
					continue
				}
				if p, ok := parseExpose(composepkg.ExpandEnv(item.Content[1].Value, env)); ok {
					svc.Ports = append(svc.Ports, p)
				}
			}
		}
		// A jail on its own LAN address has no `expose:` -- appjail refuses it
		// outright ("expose requires the following options: virtualnet"),
		// because there is no host port to forward. The compose still records
		// which port the app serves on, and Status needs it: without a port to
		// probe, health falls back to grepping the log for a crash, which
		// reports a perfectly healthy stack as crashed.
		if len(svc.Ports) == 0 {
			svc.Ports = composePortsFor(s, key, env)
		}
		out = append(out, svcJail{svc: svc, jail: jail, from: from})
	}
	return out
}

// composePortsFor returns the ports the stack's compose declares for one
// service. Only the container side is meaningful for a LAN-addressed jail --
// the app answers on its own address -- but the host side is kept so the UI
// shows the same number it would for a NAT'd jail.
func composePortsFor(s *stack.Stack, service string, env map[string]string) []composepkg.PortMap {
	if s == nil || s.Compose == "" {
		return nil
	}
	for _, svc := range composepkg.ParseServices(s.Compose, env) {
		if svc.Name == service {
			return svc.Ports
		}
	}
	return nil
}

// parseExpose reads an appjail expose spec: "HOST:CONTAINER [proto:tcp|udp]"
// or a bare "PORT" (same port both sides).
func parseExpose(spec string) (composepkg.PortMap, bool) {
	fields := strings.Fields(spec)
	if len(fields) == 0 {
		return composepkg.PortMap{}, false
	}
	p := composepkg.PortMap{Proto: "tcp"}
	host, cont, found := strings.Cut(fields[0], ":")
	if !found {
		cont = host
	}
	var err error
	if p.Host, err = strconv.Atoi(host); err != nil {
		return p, false
	}
	if p.Container, err = strconv.Atoi(cont); err != nil {
		return p, false
	}
	for _, f := range fields[1:] {
		if v, ok := strings.CutPrefix(f, "proto:"); ok {
			p.Proto = v
		}
	}
	return p, true
}

// yamlMapValue returns the value node for key in a mapping node, or nil.
func yamlMapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
