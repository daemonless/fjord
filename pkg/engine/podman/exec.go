package podman

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
)

// apiExecSession bridges a libpod exec instance's hijacked TTY stream to the
// generic engine.ExecSession. Driving the socket API directly (rather than
// shelling out to `podman exec -it`) avoids the fragile remote-CLI attach that
// dropped mid-session on FreeBSD, and the exec is torn down cleanly on Close
// (no leaked exec sessions that later wedge `compose down`).
type apiExecSession struct {
	conn      net.Conn
	rd        *bufio.Reader // may hold stream bytes buffered while reading headers
	execID    string
	container string
	argv0     string // what was exec'd; reap checks the pid still runs it
	http      *http.Client
}

func (s *apiExecSession) Read(p []byte) (int, error)  { return s.rd.Read(p) }
func (s *apiExecSession) Write(p []byte) (int, error) { return s.conn.Write(p) }

// Close tears the stream down AND reaps the exec'd process. Closing the
// hijacked conn alone does NOT end the process on FreeBSD -- it lingers,
// podman keeps the exec session "active", and container removal is refused
// ("container state improper") until something force-removes it.
//
// The reap is waited for (bounded): fjordd closes its shells on SIGTERM and
// exits right after, and a reap left running in the background never
// finished -- the shell outlived fjordd and blocked the next update.
func (s *apiExecSession) Close() error {
	err := s.conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.reap(ctx)
	return err
}

// reap kills the exec'd process (best-effort): libpod has no remove-exec
// endpoint, but exec-inspect exposes the Pid -- which IS the in-container PID
// on FreeBSD (jails share PID numbering) -- so a one-shot detached `kill -9`
// exec in the same container ends the session.
func (s *apiExecSession) reap(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/exec/"+url.PathEscape(s.execID)+"/json", nil)
	if err != nil {
		return
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var st struct {
		Running bool
		Pid     int
	}
	if json.Unmarshal(data, &st) != nil || !st.Running || st.Pid <= 1 {
		return
	}
	// The Pid libpod reports is a HOST pid. fjordd runs as root on the host,
	// so kill it directly -- right on FreeBSD (jails share the host's pid
	// space) and on Linux (where the in-container pid would differ). Only
	// when that fails (fjordd itself is in a container without the host's
	// pid namespace) fall back to a detached kill exec inside the container,
	// which is correct on FreeBSD and best-effort elsewhere.
	//
	// Confirm the pid still runs what we exec'd first. Running was true a
	// moment ago, but between that inspect and the signal the process can
	// exit and the host recycle its pid onto an unrelated daemon -- and
	// fjordd is root, so the stray SIGKILL would land. os.FindProcess never
	// errors on Unix, so it is no guard at all; the command check is.
	if runsArgv0(st.Pid, s.argv0) {
		if p, err := os.FindProcess(st.Pid); err == nil && p.Signal(syscall.SIGKILL) == nil {
			return
		}
	}
	body, _ := json.Marshal(map[string]any{
		"Cmd": []string{"kill", "-9", strconv.Itoa(st.Pid)},
	})
	resp, err = s.post(ctx, "http://d/v4.0.0/libpod/containers/"+url.PathEscape(s.container)+"/exec", bytes.NewReader(body))
	if err != nil {
		return
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	var created struct{ Id string }
	if json.Unmarshal(data, &created) != nil || created.Id == "" {
		return
	}
	resp, err = s.post(ctx, "http://d/v4.0.0/libpod/exec/"+url.PathEscape(created.Id)+"/start", strings.NewReader(`{"Detach":true}`))
	if err == nil {
		resp.Body.Close()
	}
}

// post is a JSON POST bounded by ctx.
func (s *apiExecSession) post(ctx context.Context, u string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return s.http.Do(req)
}

// runsArgv0 reports whether pid is currently running a command whose name is
// argv0 -- the identity check that makes killing a host pid safe. ps(1) is on
// both FreeBSD and Linux; if it can't answer, the caller must NOT signal.
func runsArgv0(pid int, argv0 string) bool {
	if argv0 == "" {
		return false
	}
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return false
	}
	// ps reports the full command line; compare the program name only, since
	// the exec was created with a bare name ("/bin/sh" -> "sh").
	return filepath.Base(strings.TrimPrefix(fields[0], "-")) == argv0
}

func (s *apiExecSession) Resize(cols, rows int) error {
	u := fmt.Sprintf("http://d/v4.0.0/libpod/exec/%s/resize?h=%d&w=%d", url.PathEscape(s.execID), rows, cols)
	req, err := http.NewRequest(http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Exec opens an interactive shell via the libpod exec API: create an exec
// instance on the container, then start it over a raw-dialed socket connection
// that hijacks into a bidirectional TTY stream.
func (b *Backend) Exec(ctx context.Context, opts engine.ExecOptions) (engine.ExecSession, error) {
	cmd := opts.Cmd
	if len(cmd) == 0 {
		cmd = []string{"/bin/sh"}
	}

	// 1. create the exec instance. TERM is set so full-screen apps (top, vi)
	// know the terminal type -- xterm.js on the browser side speaks xterm.
	body, _ := json.Marshal(map[string]any{
		"AttachStdin":  true,
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          true,
		"Cmd":          cmd,
		"Env":          []string{"TERM=xterm-256color"},
	})
	createURL := "http://d/v4.0.0/libpod/containers/" + url.PathEscape(opts.Container) + "/exec"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exec create: %w", err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exec create: %s: %s", resp.Status, bytes.TrimSpace(data))
	}
	var created struct{ Id string }
	if err := json.Unmarshal(data, &created); err != nil || created.Id == "" {
		return nil, fmt.Errorf("exec create: bad response: %s", bytes.TrimSpace(data))
	}

	// 2. start it over a raw connection that hijacks into the TTY stream.
	conn, err := net.Dial("unix", b.sock)
	if err != nil {
		return nil, fmt.Errorf("dial socket: %w", err)
	}
	startBody := []byte(`{"Tty":true}`)
	fmt.Fprintf(conn,
		"POST /v4.0.0/libpod/exec/%s/start HTTP/1.1\r\nHost: d\r\nContent-Type: application/json\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Length: %d\r\n\r\n",
		created.Id, len(startBody))
	if _, err := conn.Write(startBody); err != nil {
		conn.Close()
		return nil, fmt.Errorf("exec start: %w", err)
	}

	// Read the HTTP status line + headers; everything after the blank line is
	// the raw bidirectional stream (kept in rd, which may have buffered it).
	rd := bufio.NewReader(conn)
	statusLine, err := rd.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("exec start: reading status: %w", err)
	}
	if code := statusCode(statusLine); code >= 400 {
		conn.Close()
		return nil, fmt.Errorf("exec start: %s", strings.TrimSpace(statusLine))
	}
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("exec start: reading headers: %w", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	return &apiExecSession{
		conn: conn, rd: rd, execID: created.Id, container: opts.Container,
		argv0: filepath.Base(cmd[0]), http: b.http,
	}, nil
}

// statusCode parses the numeric code out of an HTTP status line
// ("HTTP/1.1 101 UPGRADED" -> 101), or 0 if it can't.
func statusCode(statusLine string) int {
	f := strings.Fields(statusLine)
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}
