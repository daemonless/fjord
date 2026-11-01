package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/daemonless/fjord/pkg/doctor"
	"github.com/daemonless/fjord/pkg/engine"
)

// wsUpgrader upgrades the interactive-shell endpoint to a WebSocket. Only the
// UI served by this daemon may open one: a page on any other site could
// otherwise get a root shell in a container (see security.go).
var wsUpgrader = websocket.Upgrader{
	CheckOrigin: sameOrigin,
}

// stackExec serves GET /api/stacks/<name>/exec?container=<c>: an interactive
// shell over a WebSocket. The bridge is engine-agnostic -- it shuttles bytes
// between the socket and an engine.ExecSession, whatever runtime created it.
// Client framing: first byte '0' = input keystrokes, '1' = resize JSON
// {Cols,Rows}.
func (s *server) stackExec(w http.ResponseWriter, r *http.Request, name string) {
	container := r.URL.Query().Get("container")
	if container == "" {
		http.Error(w, "container required", 400)
		return
	}
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	// The stack in the path did not bound the container in the query: any
	// jail on the host could be named here and got a root shell. The UI only
	// ever offers names from Status, so that is the list to hold it to.
	status, err := s.backendFor(st).Status(r.Context(), st)
	if err != nil {
		http.Error(w, "cannot list this stack's containers", 500)
		return
	}
	ok := false
	for _, c := range status.Containers {
		if c.Name == container {
			ok = true
			break
		}
	}
	if !ok {
		http.Error(w, "no container "+container+" in stack "+name, 403)
		return
	}
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error
	}
	defer conn.Close()
	sess, err := s.backendFor(st).Exec(context.Background(), engine.ExecOptions{Container: container})
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("exec failed: "+err.Error()+"\r\n"))
		// podman's API service older than podman: no shell will open until
		// it restarts. Said once with a close code the page acts on (a
		// Restart button), instead of eight reconnects printing the same.
		if detail := staleService(r.Context(), err); detail != "" {
			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(closeStaleService, trimReason(detail)))
			return
		}
		conn.WriteMessage(websocket.TextMessage, []byte(execHint(r.Context(), err)))
		return
	}
	defer sess.Close()
	defer trackShell(sess)()

	// container output -> browser, decoupled through a bounded queue. The
	// podman exec stream EOFs after ~256KB if its reader stalls (no patience
	// for backpressure -- measured), so the drain goroutine ALWAYS reads at
	// full speed; when the browser can't keep up the overflow is dropped with
	// an inline notice. A terminal is a lossy display; killing the session
	// because rendering lagged is the one wrong answer.
	outq := make(chan []byte, 256) // ~2MB of 8KB chunks
	var droppedBytes int64
	// Set when the shell never ran (the image has no /bin/sh): the browser
	// gets told once and stops reconnecting, instead of looping on the
	// runtime's "executable file not found".
	var noShell atomic.Bool

	// drain podman -> queue
	go func() {
		defer close(outq)
		var total int64
		var head []byte
		started := time.Now()
		buf := make([]byte, 8192)
		for {
			n, err := sess.Read(buf)
			if n > 0 {
				total += int64(n)
				if len(head) < noShellHead {
					head = append(head, buf[:min(n, noShellHead-len(head))]...)
				}
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case outq <- chunk:
				default:
					atomic.AddInt64(&droppedBytes, int64(n))
				}
			}
			if err != nil {
				log.Printf("exec %s: session ended after %d bytes: %v", container, total, err)
				noShell.Store(noShellOutput(head, total, time.Since(started)))
				return
			}
		}
	}()

	// queue -> browser
	go func() {
		defer conn.Close()
		defer sess.Close()
		defer func() {
			if !noShell.Load() {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			conn.WriteMessage(websocket.TextMessage, []byte("\r\nThis image has no shell, so there is nothing to open here.\r\n"))
			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(closeNoShell, "no shell"))
		}()
		for chunk := range outq {
			conn.SetWriteDeadline(time.Now().Add(60 * time.Second))
			if err := conn.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
				return
			}
			if len(outq) == 0 {
				if d := atomic.SwapInt64(&droppedBytes, 0); d > 0 {
					notice := fmt.Sprintf("\r\n\x1b[33m[fjord: output too fast to stream -- %.1f MB skipped]\x1b[0m\r\n", float64(d)/1048576)
					conn.SetWriteDeadline(time.Now().Add(60 * time.Second))
					if err := conn.WriteMessage(websocket.BinaryMessage, []byte(notice)); err != nil {
						return
					}
				}
			}
		}
	}()

	// browser input -> container
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if len(data) == 0 {
			continue
		}
		switch data[0] {
		case '0':
			sess.Write(data[1:])
		case '1':
			var rs struct{ Cols, Rows int }
			if json.Unmarshal(data[1:], &rs) == nil {
				sess.Resize(rs.Cols, rs.Rows)
			}
		}
	}
}

// closeNoShell is the WebSocket close code (app range) for a shell that
// cannot exist; the browser does not reconnect on it. closeStaleService:
// podman's API service predates its binaries; the reason carries the
// doctor's detail and the page offers the restart.
const closeNoShell = 4001
const closeStaleService = 4002

// staleService is the doctor's detail when an exec failed because podman's
// API service is older than podman, else "".
func staleService(ctx context.Context, err error) string {
	if !strings.Contains(err.Error(), "no such file or directory") {
		return ""
	}
	r, ok := doctor.RunOne(ctx, doctor.Config{Engines: []string{"podman"}}, "podman-stale")
	if !ok || r.Status != doctor.Warn {
		return ""
	}
	return r.Detail
}

// trimReason keeps a close reason inside the 123 bytes the protocol allows.
func trimReason(s string) string {
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}

// noShellHead is how much of a session's first output is kept to tell a
// shell that never started from one that ran and ended.
const noShellHead = 512

// noShellOutput says whether an exec session was the runtime refusing to
// start the shell at all: the image has no /bin/sh (scratch images such as
// traefik/whoami). crun, runc and ocijail all say "executable file ... not
// found" within a moment and then end; a real shell that said "not found"
// did so after a prompt and some time.
func noShellOutput(head []byte, total int64, ran time.Duration) bool {
	if ran > 5*time.Second || total > noShellHead*2 {
		return false
	}
	out := strings.ToLower(string(head))
	return strings.Contains(out, "not found") && (strings.Contains(out, "executable") || strings.Contains(out, "/bin/sh"))
}

// execHint is why a shell failed when doctor knows: podman upgraded under its
// running API service leaves every exec "no such file or directory" while the
// CLI works, so it reads as a fjord bug. The tab says what to run instead.
func execHint(ctx context.Context, err error) string {
	if !strings.Contains(err.Error(), "no such file or directory") {
		return ""
	}
	r, ok := doctor.RunOne(ctx, doctor.Config{Engines: []string{"podman"}}, "podman-stale")
	if !ok || r.Status != doctor.Warn {
		return ""
	}
	return "\r\npodman's API service is out of date: " + r.Detail + ".\r\nFix, as root:\r\n  " +
		strings.ReplaceAll(r.Fix, "\n", "\r\n  ") + "\r\n"
}
