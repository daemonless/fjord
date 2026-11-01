package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/stack"
)

// portView is a port the install wizard asked about that is taken: what
// holds it, and the nearest one up that is free.
type portView struct {
	HeldBy string `json:"heldBy"`
	Free   int    `json:"free,omitempty"`
}

// handleFreePorts answers GET /api/ports/free?ports=3000,3001,1900/udp with
// the taken ones, so the wizard can start on a free port instead of the
// install refusing it: every linuxserver desktop app wants 3000 and 3001.
// The same three sources as pre-flight: other containers, other stacks'
// config, something answering on the host.
func (s *server) handleFreePorts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	used, _ := s.primaryBackend().UsedPorts(ctx)
	taken := s.otherStackPorts(&stack.Stack{})
	hostAddr := os.Getenv("FJORD_HOST_ADDR")
	if hostAddr == "" {
		hostAddr = "127.0.0.1"
	}
	heldBy := func(port int, proto string) string {
		key := fmt.Sprintf("%d/%s", port, proto)
		if c, ok := used[key]; ok {
			return "container " + c
		}
		if st, ok := taken[key]; ok {
			return st
		}
		if proto == "tcp" && tcpInUse(hostAddr, port) {
			if name := listenerName(ctx, port); name != "" {
				return name + " on this host"
			}
			return "a program on this host"
		}
		return ""
	}
	type want struct {
		port  int
		proto string
	}
	var asked []want
	claimed := map[string]bool{} // the asked ports, and every one suggested
	for _, p := range strings.Split(r.URL.Query().Get("ports"), ",") {
		num, proto, _ := strings.Cut(strings.TrimSpace(p), "/")
		if proto == "" {
			proto = "tcp"
		}
		if n, err := strconv.Atoi(num); err == nil && n > 0 && n <= 65535 && (proto == "tcp" || proto == "udp") {
			asked = append(asked, want{n, proto})
			claimed[fmt.Sprintf("%d/%s", n, proto)] = true
		}
	}
	out := map[string]portView{}
	for _, a := range asked {
		holder := heldBy(a.port, a.proto)
		if holder == "" {
			continue
		}
		v := portView{HeldBy: holder}
		for p := a.port + 1; p <= 65535 && p < a.port+200; p++ {
			key := fmt.Sprintf("%d/%s", p, a.proto)
			if !claimed[key] && heldBy(p, a.proto) == "" {
				v.Free = p
				claimed[key] = true
				break
			}
		}
		out[fmt.Sprintf("%d/%s", a.port, a.proto)] = v
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// listenerName is the program listening on a TCP port of this host, "" when
// it cannot be told: sockstat on FreeBSD, ss on Linux.
func listenerName(ctx context.Context, port int) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if runtime.GOOS == "freebsd" {
		out, err := exec.CommandContext(ctx, "sockstat", "-46l", "-P", "tcp", "-p", strconv.Itoa(port)).Output()
		if err != nil {
			return ""
		}
		return sockstatCommand(string(out))
	}
	out, err := exec.CommandContext(ctx, "ss", "-Hltnp", "sport = :"+strconv.Itoa(port)).Output()
	if err != nil {
		return ""
	}
	return ssCommand(string(out))
}

// sockstatCommand is the COMMAND of sockstat's first row (after the header).
func sockstatCommand(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return ""
	}
	if f := strings.Fields(lines[1]); len(f) > 1 {
		return f[1]
	}
	return ""
}

var ssUsers = regexp.MustCompile(`users:\(\("([^"]+)"`)

// ssCommand is the program ss names first: users:(("conmon",pid=12,fd=5)).
func ssCommand(out string) string {
	if m := ssUsers.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}
