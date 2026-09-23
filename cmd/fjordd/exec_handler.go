package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

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

	// drain podman -> queue
	go func() {
		defer close(outq)
		var total int64
		buf := make([]byte, 8192)
		for {
			n, err := sess.Read(buf)
			if n > 0 {
				total += int64(n)
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
				return
			}
		}
	}()

	// queue -> browser
	go func() {
		defer conn.Close()
		defer sess.Close()
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
