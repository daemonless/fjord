// Package appjail runs stacks as FreeBSD jails through appjail-director, the
// AppJail orchestrator (the podman-compose equivalent): a stack is a directory
// holding appjail-director.yml + Makejail + .env, rendered by dbuild and
// carried in the catalog manifest, and fjord runs `appjail-director up/down`
// in it. The same unmodified OCI images podman uses are turned into jails via
// AppJail's OCI support (5.4.0+). No compose translation happens here; the
// spec is the truth, and the user can edit it. It implements engine.Backend so
// fjord drives appjail and podman identically.
//
// TODO: image-digest update checks (ImageRepoDigests).
package appjail

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/creack/pty"
	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/stack"
)

// Backend drives stacks through AppJail's OCI runtime.
type Backend struct{}

// NewBackend returns an appjail backend (nil-safe; availability is checked by
// the caller before registration).
func NewBackend() *Backend { return &Backend{} }

// Descriptor registers appjail as an engine. It can only run directly on a
// FreeBSD host (appjail manages host jails, impossible from inside one), and
// pre-5.5.0 gets a non-fatal warning since it lacks the OCI maturity some apps
// need. All of appjail's engine-specific knowledge lives here.
var Descriptor = engine.Descriptor{
	Name:        "appjail",
	Description: "Native FreeBSD jails from OCI images, orchestrated by appjail-director.",
	Package:     "appjail sysutils/py-director", // appjail + appjail-director (py-director)
	Available:   available,
	New:         func() engine.Backend { return NewBackend() },
}

func available() (ok bool, reason, warning string) {
	if runtime.GOOS != "freebsd" {
		return false, "appjail requires FreeBSD", ""
	}
	if engine.Jailed() {
		return false, "fjordd is running inside a jail; appjail cannot manage jails from within one -- run fjordd directly on the host", ""
	}
	if _, err := exec.LookPath("appjail"); err != nil {
		return false, "appjail not found in PATH (pkg install -y appjail sysutils/py-director)", ""
	}
	// Stacks run via appjail-director (the py-director package); without it
	// every Start fails, so it is a hard requirement, not a warning.
	if _, err := exec.LookPath("appjail-director"); err != nil {
		return false, "appjail-director not found in PATH (pkg install -y sysutils/py-director)", ""
	}
	if v := Version(); v != "" && versionBelow(v, 5, 5) {
		return true, "", "AppJail " + v + " is installed; 5.5.0+ is recommended. Older versions run simple apps but may not handle kernel modules, secrets, or every OCI app."
	}
	return true, "", ""
}

var (
	versionOnce sync.Once
	versionVal  string
)

// Version returns the installed appjail version (e.g. "5.5.0"), or "".
// Memoized: the engine list, the doctor and startup all ask, and `appjail
// version` is a large shell script that takes seconds. pkg's database answers
// in milliseconds, so it's asked first; the CLI is the fallback for a
// port/git install. A version change needs a fjordd restart anyway.
func Version() string {
	versionOnce.Do(func() { versionVal = probeVersion() })
	return versionVal
}

func probeVersion() string {
	if out, err := exec.Command("pkg", "query", "%v", "appjail").Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			return strings.SplitN(v, "_", 2)[0] // drop the port revision
		}
	}
	out, err := exec.Command("appjail", "version").Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(strings.TrimSpace(string(out)))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// versionBelow reports whether dotted version v is older than major.minor.
func versionBelow(v string, major, minor int) bool {
	num := func(s string) int {
		n := 0
		for _, c := range s {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) == 0 || parts[0] == "" {
		return false
	}
	if maj := num(parts[0]); maj != major {
		return maj < major
	}
	min := 0
	if len(parts) > 1 {
		min = num(parts[1])
	}
	return min < minor
}

// errNoDirector is returned for a stack dir without an appjail-director.yml:
// an appjail stack is a director project, nothing else.
func errNoDirector(s *stack.Stack) error {
	return fmt.Errorf("stack %s has no appjail-director.yml -- an appjail stack is an appjail-director project (reinstall it, or add the spec)", s.Name)
}

// Up brings the director project up: each jail is built from its Makejail,
// gets its oci: config, and starts in dependency order. Idempotent.
func (b *Backend) Up(ctx context.Context, s *stack.Stack) (io.ReadCloser, error) {
	if directorFile(s) == "" {
		return nil, errNoDirector(s)
	}
	return directorUp(ctx, s)
}

// Down tears the project down and destroys its jails.
func (b *Backend) Down(ctx context.Context, s *stack.Stack) (io.ReadCloser, error) {
	if directorFile(s) == "" {
		return nil, errNoDirector(s)
	}
	return directorDown(ctx, s)
}

// Restart stops and starts the jails in place (no rebuild, no re-pull).
func (b *Backend) Restart(ctx context.Context, s *stack.Stack) (io.ReadCloser, error) {
	if directorFile(s) == "" {
		return nil, errNoDirector(s)
	}
	return directorRestart(ctx, s)
}

// Update destroys and rebuilds the jails with a fresh image pull. Always the
// whole project: director has no per-service rebuild that fjord has verified,
// so a subset is refused rather than quietly widened to everything.
func (b *Backend) Update(ctx context.Context, s *stack.Stack, services []string) (io.ReadCloser, error) {
	if len(services) > 0 {
		return nil, fmt.Errorf("appjail updates the whole stack; it cannot update only %s", strings.Join(services, ", "))
	}
	if directorFile(s) == "" {
		return nil, errNoDirector(s)
	}
	return directorUpdate(ctx, s)
}

// Status reports each service jail's state. Ports come from the compose (appjail
// doesn't report publishings), attached only to running jails.
func (b *Backend) Status(ctx context.Context, s *stack.Stack) (engine.StackStatus, error) {
	svcs := b.serviceJails(s)
	attached, _ := composepkg.AttachedNetwork(s.Compose)
	var containers []engine.ContainerStatus
	up := 0
	jailsUp := 0
	for _, sj := range svcs {
		svc, name := sj.svc, sj.jail
		jailUp := exec.CommandContext(ctx, "appjail", "status", "-q", name).Run() == nil
		cs := engine.ContainerStatus{Name: name, Service: svc.Name, State: "stopped"}
		if jailUp {
			jailsUp++
			// A jail being up says nothing about the app inside it: a .NET
			// service missing allow.mlock crash-loops forever behind an
			// "up" jail. Ask the app itself.
			cs.State, cs.Detail = serviceHealth(ctx, name, svc)
			cs.Address = hostnet.JailAddress(ctx, name)
			// The jail is up and the app answers, but it holds no address:
			// its interface is on the bridge with nothing configured on it.
			// A link to it cannot work, and saying nothing makes that look
			// like fjord losing the address rather than the network failing.
			if cs.State == "running" && cs.Address == "" && cs.Detail == "" && attached != "" {
				cs.Detail = hostnet.NoAddressReason(attached)
				// Before blaming the DHCP server, check the jail can even ask.
				if net, ok := hostnet.Get(attached); ok && net.DHCP && missingDHClient(ctx, name) {
					cs.Detail = "no address: this image has no /etc/rc.d/dhclient, so nothing in the jail " +
						"can ask for a lease -- give it a static address, or use an image that ships one"
				}
			}
			if cs.State == "running" {
				up++
			}
			for _, p := range svc.Ports {
				cs.Ports = append(cs.Ports, engine.Port{HostPort: p.Host, ContainerPort: p.Container, Protocol: p.Proto})
			}
		}
		containers = append(containers, cs)
	}
	state := "stopped"
	switch {
	case len(svcs) == 0:
		state = "unknown"
	case up == len(svcs):
		state = "running"
	case jailsUp > 0:
		state = "partial" // something is up but not (yet) serving
	}
	return engine.StackStatus{State: state, Containers: containers}, nil
}

// crashedInLog reports whether a container log tail shows s6 restarting a
// service after a genuine fault.
//
// s6 logs a deliberate stop with the same wording it uses for a fault:
//
//	[s6] Service 'zensical' crashed (Exit: 256, Signal: 15)
//
// Signal 15 is SIGTERM -- that line IS the shutdown, so a stack that was
// stopped and started again reads as crashed for as long as it stays in the
// tail. Only faults we did not cause count.
func crashedInLog(t string) bool {
	for _, ln := range strings.Split(t, "\n") {
		if !strings.Contains(ln, "] Service '") || !strings.Contains(ln, "crashed") {
			continue
		}
		if strings.Contains(ln, "Signal: 15") || strings.Contains(ln, "Signal: 2") {
			continue // SIGTERM / SIGINT: a stop, not a fault
		}
		return true
	}
	return false
}

// serviceHealth reports whether the app inside an up jail is actually
// serving: "running" when a published port is listening (or the service
// publishes none, which leaves nothing to probe), "crashed" when nothing
// listens and the container log shows s6 restarting the service, else
// "starting" (booting, or an app that hasn't bound yet).
func serviceHealth(ctx context.Context, jail string, svc composepkg.Service) (state, detail string) {
	// s6 restarting the service in the last few log lines. Only a fallback:
	// a healthy but quiet service logs nothing new, so its tail still shows
	// the last crash before it recovered -- a listening port must override it.
	crashing := func() bool {
		f := latestLog(jail)
		if f == "" {
			return false
		}
		tail, err := exec.CommandContext(ctx, "tail", "-n", "12", f).Output()
		if err != nil {
			return false
		}
		return crashedInLog(string(tail))
	}

	// A listening published port is definitive proof the app is serving, so
	// check it FIRST, ahead of the crash log.
	if len(svc.Ports) > 0 {
		// Probe published ports from the host with sockstat -j <jid> (no jexec).
		if jidOut, err := exec.CommandContext(ctx, "jls", "-j", jail, "jid").Output(); err == nil {
			jid := strings.TrimSpace(string(jidOut))
			for _, p := range svc.Ports {
				proto := "tcp"
				if p.Proto != "" {
					proto = p.Proto
				}
				out, err := exec.CommandContext(ctx, "sockstat", "-46", "-l", "-j", jid, "-P", proto, "-p", strconv.Itoa(p.Container)).Output()
				if err == nil && strings.Count(strings.TrimSpace(string(out)), "\n") >= 1 { // header + at least one socket
					return "running", ""
				}
			}
		}
		// Nothing listening yet: a crash in the log means it's failing, else
		// it is still booting.
		if crashing() {
			return "crashed", "the app inside the jail keeps crashing -- see Logs"
		}
		return "starting", "jail is up but nothing is listening on its port yet"
	}

	// No published ports (host-network stacks, databases): the crash log is
	// the only signal we have.
	if crashing() {
		return "crashed", "the app inside the jail keeps crashing -- see Logs"
	}
	return "running", ""
}

// Logs tails each service jail's container log. follow uses tail -F; tail bounds
// the backlog. The containers filter matches jail names (from Status).
func (b *Backend) Logs(ctx context.Context, s *stack.Stack, tail int, follow bool, containers []string) (io.ReadCloser, error) {
	want := map[string]bool{}
	for _, c := range containers {
		want[c] = true
	}
	var files []string
	for _, sj := range b.serviceJails(s) {
		name := sj.jail
		if len(want) > 0 && !want[name] {
			continue
		}
		if f := latestLog(name); f != "" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		pr, pw := io.Pipe()
		pw.Close()
		return pr, nil
	}
	args := []string{"-n", strconv.Itoa(tail)}
	if follow {
		args = append(args, "-F")
	}
	args = append(args, files...)
	cmd := exec.CommandContext(ctx, "tail", args...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	go func() { _ = cmd.Run(); pw.Close() }()
	return pr, nil
}

// latestLog returns the newest container log for a jail, or "".
func latestLog(jail string) string {
	dir := filepath.Join("/var/log/appjail/jails", jail, "container")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var newestMod int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if m := info.ModTime().Unix(); m >= newestMod {
			newestMod = m
			newest = filepath.Join(dir, e.Name())
		}
	}
	return newest
}

// Networks returns empty on purpose: director gives each jail a virtualnet
// address behind NAT, not the LAN-routable own-IP the Resources picker
// promises, and the director spec does not take a per-service network from
// fjord yet. listVirtualnets() below parses them for when it does.
func (b *Backend) Networks(ctx context.Context) ([]engine.Network, error) {
	nets := []engine.Network{}
	// LAN networks are shared with podman: both engines attach to the same
	// host bridge, so "vlan5" means one thing on this host rather than a
	// conflist to one engine and a bridge to the other.
	for _, n := range hostnet.List() {
		// Only this plugin's networks: a CNI bridge conflist is a project's
		// private NAT segment, not a LAN bridge a jail can join.
		if n.Type != "epair" || n.Bridge == "" {
			continue
		}
		// Made for another engine: the segment is shared, but the form was
		// filled in for what that engine can do, so offering it here would
		// offer an address source this one cannot use.
		if n.For != "" && n.For != "appjail" {
			continue
		}
		// A DHCP network is usable: fjord takes the lease on the host at
		// install time and gives the jail a fixed address, because appjail
		// runs dhclient inside the jail and these images ship none.
		if n.Subnet == "" && !n.DHCP {
			continue
		}
		nets = append(nets, engine.Network{
			// The conflist's own type, not "bridge": a network must not change
			// identity depending on which engine describes it, and "bridge" is
			// what a private NAT network is called -- so a LAN network read as
			// a private one exactly when podman was not installed to say
			// otherwise.
			Name: n.Name, Driver: n.Type, Subnet: n.Subnet, Gateway: n.Gateway,
			UsedBy: jailsOnBridge(ctx, n.Bridge),
		})
	}
	// appjail's own NAT networks are listed so a jail already on one is not
	// invisible, but fjord neither creates nor attaches to them.
	nets = append(nets, listVirtualnets(ctx)...)
	return nets, nil
}

// jailsOnBridge names the jails with an epair on a host bridge. appjail names
// the host side "sa_<iface>" and a jail's vnet interface "sb_<iface>", so the
// bridge's member list is the attachment record.
func jailsOnBridge(ctx context.Context, bridge string) []string {
	members := bridgeMembers(ctx, bridge)
	if len(members) == 0 {
		return nil
	}
	// The bridge names INTERFACES, and an interface is not a user: the page
	// read "used by immichdataba", which is an epair trimmed to fit IFNAMSIZ
	// and not the name of anything a person installed. The jail side of that
	// epair is inside the jail, so each jail is asked what it holds.
	var out []string
	for _, jail := range runningJails(ctx) {
		for _, iface := range jailInterfaces(ctx, jail) {
			base, ok := epairPeer(iface)
			if ok && members[base] {
				out = append(out, jail)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// bridgeMembers is the set of epair base names attached to a bridge, from the
// host-side interface of each -- sa_<name> for a bridge attachment, ea_<name>
// for an appjail virtual network.
func bridgeMembers(ctx context.Context, bridge string) map[string]bool {
	out, err := exec.CommandContext(ctx, "ifconfig", bridge).Output()
	if err != nil {
		return nil
	}
	members := map[string]bool{}
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.Fields(ln)
		if len(f) < 2 || f[0] != "member:" {
			continue
		}
		for _, prefix := range []string{"sa_", "ea_"} {
			if name, ok := strings.CutPrefix(f[1], prefix); ok {
				members[name] = true
			}
		}
	}
	return members
}

// epairPeer turns a jail-side epair name into the base the host side shares.
func epairPeer(iface string) (string, bool) {
	for _, prefix := range []string{"sb_", "eb_"} {
		if name, ok := strings.CutPrefix(iface, prefix); ok {
			return name, true
		}
	}
	return "", false
}

// runningJails lists the jails that exist right now, by name.
func runningJails(ctx context.Context) []string {
	out, err := exec.CommandContext(ctx, "jls", "-h", "name").Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return nil // header only
	}
	return lines[1:]
}

// jailInterfaces is what a jail holds. ifconfig -j is the cheap way to ask:
// one exec per jail, and no need to enter it.
func jailInterfaces(ctx context.Context, jail string) []string {
	out, err := exec.CommandContext(ctx, "ifconfig", "-j", jail, "-l").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// listVirtualnets enumerates appjail's virtual networks.
// Virtualnet reports an appjail virtual network by name. A jail joins one of
// these with the `virtualnet` option, not with an epair onto a host bridge --
// two different attachments that the director writer has to tell apart.
func Virtualnet(ctx context.Context, name string) (engine.Network, bool) {
	for _, n := range listVirtualnets(ctx) {
		if n.Name == name {
			return n, true
		}
	}
	return engine.Network{}, false
}

func listVirtualnets(ctx context.Context) []engine.Network {
	// -H no header, -p tab-separated columns; keywords are space-separated args.
	out, err := exec.CommandContext(ctx, "appjail", "network", "list", "-Hp", "name", "network", "cidr", "gateway").Output()
	if err != nil {
		return nil // best-effort
	}
	var nets []engine.Network
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 4 {
			continue
		}
		subnet := f[1]
		if f[2] != "" {
			subnet = f[1] + "/" + f[2]
		}
		nets = append(nets, engine.Network{
			Name: f[0], Driver: "virtualnet", Subnet: subnet, Gateway: f[3],
		})
	}
	// Who is on each one, all at once. `appjail network hosts` costs ~340ms a
	// call -- appjail is shell -- and asking for one network at a time made
	// listing cost 160ms + 340ms x networks: a third of a second to draw the
	// page with one network, over two seconds with five. The calls do not
	// depend on each other, so the wait is now one of them rather than all of
	// them. Bounded so a host with many networks does not fork a process per
	// network at once.
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range nets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			nets[i].UsedBy = virtualnetUsers(ctx, nets[i].Name, nets[i].Gateway)
		}(i)
	}
	wg.Wait()
	return nets
}

// virtualnetUsers names the jails holding a reserved address on a virtualnet.
// `network hosts -r -H` lists "<address>\t<name>"; the network's own gateway
// entry ("<net>.appjail") is infrastructure, not a user.
func virtualnetUsers(ctx context.Context, name, gateway string) []string {
	out, err := exec.CommandContext(ctx, "appjail", "network", "hosts", "-r", "-n", name, "-H").Output()
	if err != nil {
		return nil // best-effort: RemoveNetwork still refuses via appjail itself
	}
	return parseReservedHosts(string(out), name, gateway)
}

// parseReservedHosts reads `appjail network hosts -r -H` output: tab-separated
// "<address>\t<name>", the name column padded, every name suffixed ".appjail".
// The network's own gateway row is infrastructure, not a user.
func parseReservedHosts(out, name, gateway string) []string {
	var users []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 2 {
			continue
		}
		host := strings.TrimSuffix(strings.TrimSpace(f[1]), ".appjail")
		if host == "" || strings.TrimSpace(f[0]) == gateway || host == name {
			continue
		}
		users = append(users, host)
	}
	sort.Strings(users)
	return users
}

// --- not yet implemented on appjail (honest stubs) ---

// Exec opens an interactive shell in a jail via `appjail cmd jexec`, attached
// to a pty so the WebSocket/xterm bridge behaves like a real terminal. The
// jail name is opts.Container (as reported by Status).
func (b *Backend) Exec(ctx context.Context, opts engine.ExecOptions) (engine.ExecSession, error) {
	cmdArgs := opts.Cmd
	if len(cmdArgs) == 0 {
		cmdArgs = []string{"/bin/sh"}
	}
	args := append([]string{"cmd", "jexec", opts.Container, "-l"}, cmdArgs...)
	c := exec.Command("appjail", args...)
	// TERM so full-screen apps (top, vi) work; xterm.js speaks xterm.
	c.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := pty.Start(c)
	if err != nil {
		return nil, fmt.Errorf("jexec %s: %w", opts.Container, err)
	}
	return &ptySession{cmd: c, pty: f}, nil
}

// ptySession bridges a pty-attached jexec process to engine.ExecSession.
type ptySession struct {
	cmd *exec.Cmd
	pty *os.File
}

func (s *ptySession) Read(p []byte) (int, error)  { return s.pty.Read(p) }
func (s *ptySession) Write(p []byte) (int, error) { return s.pty.Write(p) }

func (s *ptySession) Resize(cols, rows int) error {
	return pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Close kills the shell and reaps it so no jexec process leaks.
func (s *ptySession) Close() error {
	s.pty.Close()
	if s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
	return nil
}

// Volumes: appjail has no podman-style named volumes; storage is fstab/pseudofs.
func (b *Backend) Volumes(ctx context.Context) ([]engine.Volume, error) {
	return []engine.Volume{}, nil
}

func (b *Backend) CreateVolume(ctx context.Context, spec engine.VolumeSpec) (engine.Volume, error) {
	return engine.Volume{}, fmt.Errorf("named volumes are not supported on the appjail engine")
}

func (b *Backend) RemoveVolume(ctx context.Context, name string, force bool) error {
	return fmt.Errorf("named volumes are not supported on the appjail engine")
}

// UsedPorts: appjail publishings aren't easily enumerable yet -- best-effort empty.
func (b *Backend) UsedPorts(ctx context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

// RunningImages reads the image each service's jail was built from.
//
// An OCI jail's root filesystem is a buildah container named
// "appjail-<jail>" (see jailBacked), and buildah records what it was created
// from: the ref, the image ID and the digest it was pulled as. That is the
// same thing podman keeps on a container, so the update check judges jails
// by what they run too. It is also the only place a director stack names its
// image at all -- the director spec names a makejail, and the makejail's
// OPTION from= is fetched from GitHub at build time.
func (b *Backend) RunningImages(ctx context.Context, s *stack.Stack) ([]engine.RunningImage, error) {
	var out []engine.RunningImage
	for _, sj := range b.serviceJails(s) {
		raw, err := exec.CommandContext(ctx, "buildah", "inspect", "--type", "container", "appjail-"+sj.jail).Output()
		if err != nil {
			continue // not an OCI jail, or not created yet
		}
		var c struct {
			FromImage       string `json:"FromImage"`
			FromImageID     string `json:"FromImageID"`
			FromImageDigest string `json:"FromImageDigest"`
		}
		if json.Unmarshal(raw, &c) != nil || c.FromImage == "" {
			continue
		}
		ri := engine.RunningImage{Service: sj.svc.Name, ImageID: c.FromImageID, Ref: c.FromImage, Digest: c.FromImageDigest}
		if c.FromImageDigest != "" {
			ri.Digests = []string{c.FromImageDigest}
		}
		out = append(out, ri)
	}
	return out, nil
}

// ImageRepoDigests reads a local image's digest from `buildah images`, which
// reports the digest the image was pulled by -- the index digest for a
// multi-arch tag, i.e. what the registry reports for the tag and what the
// update check compares against. Not pulled -> nil, nil.
func (b *Backend) ImageRepoDigests(ctx context.Context, ref string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "buildah", "images", "--json", ref).Output()
	if err != nil {
		return nil, nil // buildah exits non-zero for an unknown image
	}
	var imgs []struct {
		Names  []string `json:"names"`
		Digest string   `json:"digest"`
	}
	if err := json.Unmarshal(out, &imgs); err != nil {
		return nil, fmt.Errorf("parse buildah images: %w", err)
	}
	var digests []string
	for _, im := range imgs {
		if im.Digest == "" {
			continue
		}
		repo := ref
		if i := strings.LastIndex(repo, "@"); i >= 0 {
			repo = repo[:i]
		} else if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
			repo = repo[:i]
		}
		digests = append(digests, repo+"@"+im.Digest)
	}
	return digests, nil
}
