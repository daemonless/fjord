package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/engine/podman"
	"github.com/daemonless/fjord/pkg/history"
	"github.com/daemonless/fjord/pkg/hostnet"
)

// Switching podman between its CNI and netavark network backends, both
// ways, from the System page: a safety net for a podman 5 host trying
// netavark. The host side is netavark-migrate's (switch, back); fjord then
// makes the networks again (carryOver, carryBack) and starts the stacks the
// way it does after a reboot. Podman 6 has no CNI, so no way back there.

// netSwitch is the one switch that may run at a time, and its output, kept
// for a page opened later.
type netSwitch struct {
	mu      sync.Mutex
	running bool
	to      string
	out     bytes.Buffer
	result  string // "" while running or never run; "ok", "refused", "failed"
	at      time.Time
}

var nvSwitch netSwitch

// switchBusy is why a network or stack change must wait, "" when it need
// not: while podman moves between backends, a network made or a stack
// started lands half on the old one.
func switchBusy() string {
	nvSwitch.mu.Lock()
	defer nvSwitch.mu.Unlock()
	if nvSwitch.running {
		return "podman is switching network backends; try again when it is done (System page)"
	}
	return ""
}

func (n *netSwitch) Write(p []byte) (int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.out.Write(p)
}

// podmanMajor is podman's major version, 0 when it cannot tell.
var podmanMajor = func(ctx context.Context) int {
	out, err := exec.CommandContext(ctx, "podman", "--version").Output()
	if err != nil {
		return 0
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return 0
	}
	maj, _, _ := strings.Cut(f[len(f)-1], ".")
	n, _ := strconv.Atoi(maj)
	return n
}

// podmanBackend is the network backend podman reports, "" when it does not.
var podmanBackend = func(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "podman", "info", "--format", "{{.Host.NetworkBackend}}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// switchGuard refuses a network change (anything but a read) while a switch
// runs.
func switchGuard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if why := switchBusy(); why != "" {
				http.Error(w, why, http.StatusConflict)
				return
			}
		}
		h(w, r)
	}
}

// netSwitchState is what the System page shows.
type netSwitchState struct {
	Backend   string   `json:"backend"`
	Podman    int      `json:"podman"`
	Tool      bool     `json:"tool"`
	CanSwitch bool     `json:"canSwitch"`
	CanBack   bool     `json:"canBack"`
	Note      string   `json:"note,omitempty"`
	Stacks    []string `json:"stacks"`
	Networks  []string `json:"networks"`
	Running   bool     `json:"running"`
	To        string   `json:"to,omitempty"`
	Result    string   `json:"result,omitempty"`
	Log       string   `json:"log,omitempty"`
}

// switchPlan is what a switch would restart and make again.
func (s *server) switchPlan() (stacks, networks []string) {
	if list, err := s.manager.List(); err == nil {
		for _, st := range list {
			if st.EngineName() == "podman" && st.State != nil && st.State.DesiredState == "running" {
				stacks = append(stacks, st.Name)
			}
		}
	}
	for _, d := range hostnet.List() {
		if d.Compose || d.Name == "podman" || (d.For != "" && d.For != "podman") {
			continue
		}
		networks = append(networks, d.Name)
	}
	return stacks, networks
}

func (s *server) netSwitchState(ctx context.Context) netSwitchState {
	st := netSwitchState{Podman: podmanMajor(ctx), Backend: podmanBackend(ctx)}
	// switch and back leave the stacks to fjord: --no-start is needed.
	st.Tool = runtime.GOOS == "freebsd" && migrateUsable(ctx) && migrateHas(ctx, "no-start")
	switch {
	case runtime.GOOS != "freebsd":
	case st.Podman >= 6:
		if st.Backend == "netavark" {
			st.Note = "Podman 6 has no CNI network backend, so this host stays on netavark. Going back to CNI means installing podman 5 first."
		}
	case !st.Tool:
		st.Note = "Switching needs netavark-migrate, which comes with the netavark package (net/netavark)."
	default:
		st.CanSwitch = st.Backend == "cni"
		st.CanBack = st.Backend == "netavark"
	}
	st.Stacks, st.Networks = s.switchPlan()
	nvSwitch.mu.Lock()
	st.Running, st.To, st.Result, st.Log = nvSwitch.running, nvSwitch.to, nvSwitch.result, nvSwitch.out.String()
	nvSwitch.mu.Unlock()
	if st.Running {
		st.CanSwitch, st.CanBack = false, false
	}
	return st
}

// GET /api/system/netavark
func (s *server) handleNetavarkSwitchState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.netSwitchState(ctx))
}

// POST /api/system/netavark/switch {"to":"netavark"|"cni"}: the switch runs
// on whether or not the page stays; with ?stream=1 the answer is its output
// as it happens, ending in "[done]" or "[error] <why>".
func (s *server) handleNetavarkSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.To != "netavark" && req.To != "cni") {
		http.Error(w, `to must be "netavark" or "cni"`, 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	state := s.netSwitchState(ctx)
	cancel()
	if (req.To == "netavark" && !state.CanSwitch) || (req.To == "cni" && !state.CanBack) {
		why := state.Note
		if why == "" {
			why = fmt.Sprintf("podman is on %s already, or a switch is running", state.Backend)
		}
		http.Error(w, why, http.StatusConflict)
		return
	}
	nvSwitch.mu.Lock()
	if nvSwitch.running {
		nvSwitch.mu.Unlock()
		http.Error(w, "a switch is already running", http.StatusConflict)
		return
	}
	nvSwitch.running, nvSwitch.to, nvSwitch.result, nvSwitch.at = true, req.To, "", time.Now()
	nvSwitch.out.Reset()
	nvSwitch.mu.Unlock()

	done := make(chan struct{})
	pr, pw := io.Pipe()
	go func() {
		defer close(done)
		defer pw.Close()
		out := io.MultiWriter(pw, &nvSwitch)
		result := s.runNetSwitch(out, req.To, state.Stacks)
		nvSwitch.mu.Lock()
		nvSwitch.running, nvSwitch.result = false, result
		nvSwitch.mu.Unlock()
	}()
	if r.URL.Query().Get("stream") != "1" {
		// Drained so the job never blocks on the pipe; the page reads the log.
		go io.Copy(io.Discard, pr)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	// A closed tab stops the copy, not the job.
	io.Copy(flushWriter{w}, pr)
	go io.Copy(io.Discard, pr)
	<-done
}

// runNetSwitch does the switch, writing what happens to out as sentences.
// Returns "ok", "refused" (nothing changed) or "failed".
func (s *server) runNetSwitch(out io.Writer, to string, stacks []string) string {
	say := func(format string, a ...any) { fmt.Fprintf(out, format+"\n", a...) }
	cmd := "switch"
	if to == "cni" {
		cmd = "back"
	}
	say("Moving podman to its %s network backend. The stacks on podman stop while it moves and start again after.", map[string]string{"netavark": "netavark", "cni": "CNI"}[to])
	log.Printf("netavark switch: %s (%d stacks)", cmd, len(stacks))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	rep, code, err := migrateSwitch(ctx, cmd, out)
	if err != nil {
		say("[error] netavark-migrate %s did not run: %v", cmd, err)
		return "failed"
	}
	if code == 1 {
		for _, b := range rep.Blockers {
			say("Not done: %s", b)
		}
		say("[error] netavark-migrate refused, so nothing was changed.")
		return "refused"
	}
	for _, line := range rep.did() {
		say("%s", line)
	}
	for _, n := range rep.Networks {
		switch {
		case n.Error != "":
			say("Network %s: %s", n.Name, n.Error)
		case n.Result == "created" || n.Result == "restored":
			say("Network %s is ready on the new backend.", n.Name)
		}
	}
	if len(rep.KeptNetavark) > 0 {
		say("Kept to be made again under CNI: %s.", strings.Join(rep.KeptNetavark, ", "))
	}
	// podman re-reads its backend; fjord's carry-over makes the rest again.
	podman.ForgetNetworkBackend()
	// What the notice of the backend it left says is no longer so.
	if to == "cni" {
		s.dropHostNotice(netavarkNotice)
	} else {
		s.dropHostNotice(cniNotice)
	}
	for _, c := range rep.NeedRecreate {
		if err := exec.CommandContext(ctx, "podman", "rm", "-f", c).Run(); err == nil {
			say("Removed container %s so it is made again on the new backend; its data is in its volumes.", c)
		}
	}
	failed := 0
	for _, name := range stacks {
		if why := s.upNow(name, "start", history.ByYou); why != "" {
			failed++
			say("%s did not start: %s", name, why)
		} else {
			say("%s is running.", name)
		}
	}
	// The networks were made again: an open page reloads its list.
	if s.events != nil {
		s.events.publish([]byte(`{"type":"networks"}`))
	}
	switch {
	case code == 2 || len(rep.Errors) > 0:
		for _, e := range rep.Errors {
			say("Problem: %s", e)
		}
		say("[error] the backend moved, but not everything came back. The lines above say what.")
		return "failed"
	case failed > 0:
		say("[error] the backend moved; %d stack(s) did not start.", failed)
		return "failed"
	}
	say("[done]")
	return "ok"
}

// migrateSwitch runs `netavark-migrate switch|back --json --no-start`, its
// own log going to out as it happens.
func migrateSwitch(ctx context.Context, cmd string, out io.Writer) (*migrateReport, int, error) {
	var stdout bytes.Buffer
	c := exec.CommandContext(ctx, migrateTool, cmd, "--json", "--no-start")
	c.Stdout, c.Stderr = &stdout, out
	// switch and back restart podman_service, which inherits the tool's
	// output and never closes it: once the tool has exited, move on.
	c.WaitDelay = migrateWaitDelay
	code := 0
	if err := c.Run(); err != nil {
		var exit *exec.ExitError
		switch {
		case errors.As(err, &exit):
			code = exit.ExitCode()
		case errors.Is(err, exec.ErrWaitDelay):
			code = c.ProcessState.ExitCode()
		default:
			return nil, 0, err
		}
	}
	var rep migrateReport
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil || rep.Version < 2 {
		return nil, code, fmt.Errorf("netavark-migrate %s (exit %d) gave no report", cmd, code)
	}
	rep.ExitCode = code
	return &rep, code, nil
}
