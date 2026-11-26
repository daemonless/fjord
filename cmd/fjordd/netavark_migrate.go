package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// The netavark port ships netavark-migrate, which sets a FreeBSD host up for
// netavark: pf's anchors, net.pf.filter_local, pf at boot, and podman's DNS
// server off port 53 when something holds it. When it is there and speaks
// interface 2, fjord has it do the host side, so a host fjord set up and one
// set up by hand end the same. fjord's own fixes stay for a host without it.

// migrateTool is where the port installs it; tests point it elsewhere.
var migrateTool = "/usr/local/sbin/netavark-migrate"

// migrateWaitDelay bounds the wait for the tool's output after it exits:
// podman_service, restarted by it, inherits the pipe and never closes it.
var migrateWaitDelay = 10 * time.Second

// runMigrate runs the tool and returns its stdout and exit code; err only
// when it could not run at all.
var runMigrate = func(ctx context.Context, args ...string) ([]byte, int, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, migrateTool, args...)
	cmd.Stdout = &out
	// A service the tool restarts can keep its output open; once the tool
	// has exited, do not wait on that for longer than this.
	cmd.WaitDelay = migrateWaitDelay
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.Bytes(), exit.ExitCode(), nil
	}
	return out.Bytes(), 0, err
}

// migrateUsable says whether the tool is installed and does what fjord needs:
// "netavark-migrate 2 host json no-start" or later.
func migrateUsable(ctx context.Context) bool {
	if _, err := os.Stat(migrateTool); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, code, err := runMigrate(ctx, "version")
	if err != nil || code != 0 {
		return false
	}
	f := strings.Fields(string(out))
	if len(f) < 2 || f[0] != "netavark-migrate" {
		return false
	}
	if v, err := strconv.Atoi(f[1]); err != nil || v < 2 {
		return false
	}
	has := func(c string) bool {
		for _, x := range f[2:] {
			if x == c {
				return true
			}
		}
		return false
	}
	return has("host") && has("json")
}

type migrateStep struct {
	Changed bool   `json:"changed"`
	Status  string `json:"status"`
	Detail  string `json:"detail"`
}

// migrateReport is the part of netavark-migrate's --json report fjord reads.
type migrateReport struct {
	Version  int         `json:"version"`
	OK       bool        `json:"ok"`
	ExitCode int         `json:"exit_code"`
	Changed  bool        `json:"changed"`
	Pf       migrateStep `json:"pf"`
	Sysctl   migrateStep `json:"sysctl"`
	Loader   migrateStep `json:"loader"`
	DNS      struct {
		Changed bool   `json:"changed"`
		Status  string `json:"status"`
		Port    int    `json:"port"`
		File    string `json:"file"`
	} `json:"dns_bind_port"`
	Blockers []string `json:"blockers"`
	Warnings []string `json:"warnings"`
	Errors   []string `json:"errors"`
	Backups  struct {
		PfConf string `json:"pf_conf"`
	} `json:"backups"`
}

// migrateHost runs `netavark-migrate host --json`. Exit 1 (refused, nothing
// changed) and 2 (part of it done) still come with a report.
func migrateHost(ctx context.Context) (*migrateReport, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, code, err := runMigrate(ctx, "host", "--json")
	if err != nil {
		return nil, err
	}
	var rep migrateReport
	if err := json.Unmarshal(out, &rep); err != nil || rep.Version < 2 {
		return nil, fmt.Errorf("netavark-migrate host (exit %d) gave no report", code)
	}
	rep.ExitCode = code
	return &rep, nil
}

// did says, one line each, what the run changed and how to see or undo it.
func (r *migrateReport) did() []string {
	var out []string
	if r.Pf.Changed {
		line := "Added netavark's anchors to /etc/pf.conf and loaded them."
		if r.Backups.PfConf != "" {
			line += " The file as it was is kept as " + r.Backups.PfConf + "."
		}
		out = append(out, line)
	}
	if r.Sysctl.Changed {
		out = append(out, "Turned on net.pf.filter_local, now and in /etc/sysctl.conf, so a published port also answers on this host's own addresses.")
	}
	if r.Loader.Changed {
		out = append(out, `pf now loads at boot (pf_load="YES" in /boot/loader.conf).`)
	}
	if r.DNS.Changed {
		out = append(out, fmt.Sprintf("Port 53 is taken on this host, so podman's DNS server now listens on %d (set in %s).", r.DNS.Port, r.DNS.File))
	}
	return out
}

// problems is what it could not do, for the log.
func (r *migrateReport) problems() string {
	return strings.Join(append(append(append([]string{}, r.Blockers...), r.Errors...), r.Warnings...), "; ")
}
