// Package doctor probes the host for everything fjord's engines need --
// binaries, the runtime socket, kernel facilities -- and reports each as a
// structured pass/fail with a remediation. It exists because a mis-provisioned
// host fails cryptically at stack-up time (a missing conmon or a stopped
// podman socket surfaces as a bare error from every stack); the doctor names
// the real problem up front.
//
// Platform knowledge lives only in the platform_<os>.go leaves: each assembles
// its check list from the portable probes in checks.go. Supporting a new OS
// means adding a leaf, never touching this core.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	OK      Status = "ok"
	Warn    Status = "warn"    // degraded: some features won't work
	Fail    Status = "fail"    // the active engine cannot run stacks
	Unknown Status = "unknown" // host state fjordd cannot observe from here
)

// Check is one probe plus its remediation metadata. Checks are data, assembled
// per-platform, so fix commands and package names are OS-specific while the
// core and the API stay OS-agnostic.
type Check struct {
	ID       string
	Name     string
	Engine   string // run only when this engine is active; "" = always
	HostOnly bool   // probes host state invisible from inside a container/jail
	// Group is where the setup page lists it, in plain words: "" (the engine
	// itself), "networking", "firewall" or "extras".
	Group string
	// Optional checks never count as a problem: fjord works without them,
	// minus one feature (LAN addresses, makejails from GitHub).
	Optional bool
	// Action is what the Install button does, in plain words ("Start
	// podman's API service"); a package check defaults to "Install <pkg>".
	Action string
	Probe  func(ctx context.Context) (Status, string)
	// Why explains what the check protects, in one or two plain sentences --
	// shown next to every check so an operator knows what a red row costs.
	Why string
	// Fix is a shell snippet, one command per line ("#" lines are comments),
	// reported when not ok. It must paste into a root shell as-is: no prose,
	// no "(optional)" asides -- those belong in Why.
	Fix string
	Pkg map[string]string // GOOS -> package name: installed by Install when set
	// Install puts what the check wants in place, for the Install button,
	// writing what it does to w as it goes; nil with a Pkg entry means "pkg
	// install" that package.
	Install func(ctx context.Context, w io.Writer) error
	// Commands are what Install runs, shown before anyone clicks it. A
	// package check's is its "pkg install" line.
	Commands []string
	// Installed, when set, says the package is already there and the check
	// is failing for another reason (too old, not running): installing again
	// changes nothing, so there is no Install button.
	Installed func() bool
}

// Result is the wire form of one executed check.
type Result struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Engine   string `json:"engine,omitempty"`
	Group    string `json:"group,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	Action   string `json:"action,omitempty"`
	// Commands fjord runs for the Install button, one per line; Action and
	// Commands are there whenever the check has an installer, ok or not.
	Commands []string `json:"commands,omitempty"`
	Status   Status   `json:"status"`
	Detail   string   `json:"detail,omitempty"`
	Why      string   `json:"why,omitempty"`
	Fix      string   `json:"fix,omitempty"`
	// Installable: fjord can fix this itself (the check has an installer or
	// a package, and fjordd runs on the host). The UI offers a button.
	Installable bool `json:"installable,omitempty"`
}

// Report is the full doctor output. Mode is "host", "container", or "unknown";
// anything but "host" means fjordd cannot repair the machine it manages, so
// remediations are commands for the operator, not actions fjordd can take.
// Unknown deliberately gets container semantics -- never offer a fix path we
// might apply to the wrong place.
type Report struct {
	OS         string   `json:"os"`
	Mode       string   `json:"mode"`
	CanInstall bool     `json:"canInstall"`
	Checks     []Result `json:"checks"`
}

// Config carries the runtime context checks depend on.
type Config struct {
	// Engines are the engines to report on: a check tied to one runs when that
	// engine is listed. Plural because a host can have several installed, and
	// the setup page has to say whether EACH is healthy -- filtering to the
	// default meant a working podman went unreported the moment appjail was
	// the default, on a host where stacks could be installed on either.
	Engines   []string
	FjordRoot string
}

// forEngine reports whether a check tied to an engine should run.
func (c Config) forEngine(name string) bool {
	if name == "" {
		return true // not engine-specific
	}
	for _, e := range c.Engines {
		if e == name {
			return true
		}
	}
	return false
}

// platformInfo is what each platform_<os>.go leaf provides.
type platformInfo struct {
	os         string
	mode       string // "host" | "container" | "unknown"
	canInstall bool   // a package manager is present (mode still gates use)
	checks     []Check
}

// Mode reports the deployment mode ("host", "container", "unknown") without
// running the full check suite -- for features that only apply on the host
// (e.g. the host filesystem browser).
func Mode() string {
	return platform(Config{}).mode
}

// Run executes every check applicable to this platform and the engines named.
func Run(ctx context.Context, cfg Config) Report {
	p := platform(cfg)
	results := make([]Result, 0, len(p.checks))
	for _, c := range p.checks {
		if !cfg.forEngine(c.Engine) {
			continue
		}
		var st Status
		var detail string
		if c.HostOnly && p.mode != "host" {
			st = Unknown
			detail = "fjordd is not running directly on the host and cannot verify this -- check manually"
		} else {
			st, detail = c.Probe(ctx)
		}
		r := Result{ID: c.ID, Name: c.Name, Engine: c.Engine, Group: c.Group, Optional: c.Optional, Status: st, Detail: detail, Why: c.Why}
		// What fjord would run, reported even when there is nothing to do:
		// the setup page lists every check in the order it sets things up.
		r.Action, r.Commands = c.Action, c.Commands
		if pkg := c.Pkg[p.os]; c.Install == nil && pkg != "" {
			if r.Action == "" {
				r.Action = "Install " + strings.Join(strings.Fields(pkg), " and ")
			}
			r.Commands = []string{pkgCommand(pkg)}
		}
		if st != OK {
			r.Fix = c.Fix
			r.Installable = p.canInstall && p.mode == "host" && (c.Install != nil || c.Pkg[p.os] != "") &&
				(c.Installed == nil || !c.Installed())
		}
		results = append(results, r)
	}
	return Report{
		OS:         p.os,
		Mode:       p.mode,
		CanInstall: p.canInstall && p.mode == "host",
		Checks:     results,
	}
}

// Install runs the installer of check id: its own, or "pkg install" of its
// package. Only on the host -- from inside a container there is nothing of
// the host's to install into.
func Install(ctx context.Context, cfg Config, id string, w io.Writer) error {
	p := platform(cfg)
	if p.mode != "host" {
		return fmt.Errorf("fjordd is not running directly on the host, so it cannot install anything there")
	}
	installMu.Lock()
	defer installMu.Unlock()
	for _, c := range p.checks {
		if c.ID != id {
			continue
		}
		if c.Install != nil {
			return c.Install(ctx, w)
		}
		pkg := c.Pkg[p.os]
		if pkg == "" || !p.canInstall {
			return fmt.Errorf("%s has no installer: follow its fix by hand", c.Name)
		}
		return pkgInstall(ctx, pkg, w)
	}
	return fmt.Errorf("no check %q", id)
}

// installMu runs installs one at a time: pkg holds a lock on its database, so
// a second install started meanwhile (several Install buttons clicked at once)
// fails with "Cannot get an advisory lock" instead of waiting its turn.
var installMu sync.Mutex

// PkgInstall installs packages ("appjail sysutils/py-director"), after any
// install fjordd already has running.
func PkgInstall(ctx context.Context, pkgs string, w io.Writer) error {
	installMu.Lock()
	defer installMu.Unlock()
	return pkgInstall(ctx, pkgs, w)
}

func pkgCommand(pkgs string) string { return "pkg install -y " + pkgs }

func pkgInstall(ctx context.Context, pkgs string, w io.Writer) error {
	// And wait for a pkg someone runs in a shell, rather than failing after
	// pkg's default five tries.
	return runShown(ctx, w, []string{"LOCK_RETRIES=120"}, "pkg", append([]string{"install", "-y"}, strings.Fields(pkgs)...)...)
}

// runShown echoes a command to w the way a shell would, then runs it with its
// output going to w too, so the setup page shows a terminal session.
func runShown(ctx context.Context, w io.Writer, env []string, name string, args ...string) error {
	line := strings.Join(append([]string{name}, args...), " ")
	fmt.Fprintf(w, "$ %s\n", line)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = w, w
	// A service's rc script starts its daemon in the background (daemon(8)),
	// and the daemon keeps our output pipe open for as long as it runs: Run
	// would wait for that forever after `service` itself has exited 0
	// (appjail-dns hung the setup page this way). Stop reading once the
	// command is gone; its exit status is what counts.
	cmd.WaitDelay = outputDrain
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("%s: %v", line, err)
	}
	return nil
}

// outputDrain is how long runShown keeps reading after the command exits.
var outputDrain = 3 * time.Second
