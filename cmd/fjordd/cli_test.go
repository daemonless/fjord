package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

type cliMockBackend struct {
	engine.Backend
	mu        sync.Mutex
	upStream  string
	stopped   bool
	downCalls int
	upCalls   int
	restCalls int
	updCalls  int
	updSvcs   []string
	logCalls  int
	logCtrs   []string
	pruneOpts engine.PruneOptions
	execIn    bytes.Buffer
}

func (b *cliMockBackend) Name() string { return "podman" }

func (b *cliMockBackend) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		UpdateServices:  true,
		PinServices:     true,
		RestartPolicies: []string{"unless-stopped", "always", "on-failure", "no"},
		RestartOnExit:   true,
	}
}

func (b *cliMockBackend) Status(_ context.Context, st *stack.Stack) (engine.StackStatus, error) {
	b.mu.Lock()
	stopped := b.stopped
	b.mu.Unlock()
	if stopped {
		return engine.StackStatus{State: "stopped"}, nil
	}
	return engine.StackStatus{
		State: "running",
		Containers: []engine.ContainerStatus{
			{
				Name:    st.Name + "_radarr_1",
				Service: "radarr",
				State:   "running",
				Ports:   []engine.Port{{HostPort: 7878, ContainerPort: 7878, Protocol: "tcp"}},
			},
		},
	}, nil
}

func (b *cliMockBackend) Networks(context.Context) ([]engine.Network, error) { return nil, nil }
func (b *cliMockBackend) NetworkParents(context.Context) ([]engine.NetworkParent, error) {
	return nil, nil
}
func (b *cliMockBackend) UsedPorts(context.Context) (map[string]string, error) { return nil, nil }
func (b *cliMockBackend) RunningImages(context.Context, *stack.Stack) ([]engine.RunningImage, error) {
	return []engine.RunningImage{
		{Service: "radarr", Ref: "ghcr.io/daemonless/radarr:latest", Digest: "sha256:newdigest"},
	}, nil
}

func (b *cliMockBackend) Up(_ context.Context, _ *stack.Stack) (io.ReadCloser, error) {
	b.mu.Lock()
	b.upCalls++
	out := b.upStream
	b.mu.Unlock()
	if out == "" {
		out = "[fjord] radarr has stayed up for 30s\n"
	}
	return io.NopCloser(strings.NewReader(out)), nil
}

func (b *cliMockBackend) Down(_ context.Context, _ *stack.Stack) (io.ReadCloser, error) {
	b.mu.Lock()
	b.downCalls++
	b.mu.Unlock()
	return io.NopCloser(strings.NewReader("$ podman compose down\n")), nil
}

func (b *cliMockBackend) Restart(_ context.Context, _ *stack.Stack) (io.ReadCloser, error) {
	b.mu.Lock()
	b.restCalls++
	b.mu.Unlock()
	return io.NopCloser(strings.NewReader("$ podman compose restart\n")), nil
}

func (b *cliMockBackend) Update(_ context.Context, _ *stack.Stack, svcs []string) (io.ReadCloser, error) {
	b.mu.Lock()
	b.updCalls++
	b.updSvcs = append([]string(nil), svcs...)
	out := b.upStream
	b.mu.Unlock()
	if out == "" {
		out = "[fjord] updated radarr\n"
	}
	return io.NopCloser(strings.NewReader(out)), nil
}

func (b *cliMockBackend) Logs(_ context.Context, _ *stack.Stack, _ int, _ bool, ctrs []string) (io.ReadCloser, error) {
	b.mu.Lock()
	b.logCalls++
	b.logCtrs = append([]string(nil), ctrs...)
	b.mu.Unlock()
	return io.NopCloser(strings.NewReader("2026-11-04 [error] app log line that is not a daemon failure\n")), nil
}

func (b *cliMockBackend) Prune(_ context.Context, opts engine.PruneOptions) (engine.PruneReport, error) {
	b.mu.Lock()
	b.pruneOpts = opts
	b.mu.Unlock()
	return engine.PruneReport{Reclaimed: "1.2GB", Output: "deleted images"}, nil
}

type mockExecSession struct {
	out io.Reader
	in  *bytes.Buffer
}

func (s *mockExecSession) Read(p []byte) (int, error)  { return s.out.Read(p) }
func (s *mockExecSession) Write(p []byte) (int, error) { return s.in.Write(p) }
func (s *mockExecSession) Resize(int, int) error       { return nil }
func (s *mockExecSession) Close() error                { return nil }

func (b *cliMockBackend) Exec(_ context.Context, _ engine.ExecOptions) (engine.ExecSession, error) {
	return &mockExecSession{
		out: strings.NewReader("hello-from-shell\r\n"),
		in:  &b.execIn,
	}, nil
}

func setupCLITestServer(t *testing.T) (string, *server, *cliMockBackend) {
	t.Helper()
	// Use /tmp for short Unix socket path (macOS/FreeBSD sun_path limit).
	sockDir, err := os.MkdirTemp("/tmp", "fjord-cli-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })

	root := t.TempDir()
	stacksDir := filepath.Join(root, "stacks")
	if err := os.MkdirAll(stacksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	appDataDir := filepath.Join(root, "containers")
	if err := updateSettings(root, func(st *savedSettings) {
		st.AppData = []string{appDataDir}
	}); err != nil {
		t.Fatal(err)
	}
	cat := seedTestCatalog(t, root, "daemonless", []map[string]any{
		{
			"id":           "radarr",
			"name":         "Radarr",
			"manifest_url": "/catalog/daemonless/manifests/radarr.yaml",
			"variants": []map[string]any{
				{"id": "latest", "label": "Latest", "default": true, "image": "ghcr.io/daemonless/radarr"},
			},
		},
	}, map[string]string{
		"radarr": radarrPlanManifest,
	})
	be := &cliMockBackend{}
	mgr := stack.NewManager(stacksDir)
	srv := &server{
		manager:   mgr,
		backends:  map[string]engine.Backend{"podman": be},
		defEngine: "podman",
		cat:       cat,
		fjordRoot: root,
		stacksDir: stacksDir,
		events:    newEventHub(),
		seen:      newFirstSeen(root),
		live:      newLiveStatus(),
	}
	gate, err := newAuthGate(root, ":3567", "password", "")
	if err != nil {
		t.Fatal(err)
	}
	// Create an account so HTTP requests without a session are rejected (401),
	// proving that the Unix socket bypasses browser session auth cleanly.
	if err := gate.store.setAccount("admin", "correct horse battery", "", "host", false); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	srv.routes(mux)
	gate.routes(mux)
	top := http.NewServeMux()
	top.Handle("/api/", guardMutations(mux))
	top.Handle("/catalog/", mux)
	handler := guardHost(newHostAllow("netlab", "", ""), gate.wrap(top))

	sockPath := filepath.Join(sockDir, "fjordd.sock")
	go serveLocalSocket(sockPath, handler)
	for range 50 {
		if st, err := os.Stat(sockPath); err == nil && st.Mode().Perm() == 0o600 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return sockPath, srv, be
}

func runCLIWithSocket(t *testing.T, sockPath string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("FJORD_SOCKET", sockPath)
	t.Setenv("FJORD_URL", "")
	var out, errBuf bytes.Buffer
	code := runCLI(append([]string{"fjord"}, args...), strings.NewReader(stdin), &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func TestIsCLIInvocation(t *testing.T) {
	if !isCLIInvocation([]string{"/usr/local/bin/fjord", "ls"}) {
		t.Fatal("argv[0]=fjord should dispatch to CLI")
	}
	if !isCLIInvocation([]string{"/usr/local/bin/fjord"}) {
		t.Fatal("bare argv[0]=fjord should dispatch to CLI")
	}
	if !isCLIInvocation([]string{"fjordd", "status", "radarr"}) {
		t.Fatal("fjordd status should dispatch to CLI")
	}
	if isCLIInvocation([]string{"fjordd", "--listen", ":3567"}) {
		t.Fatal("fjordd --listen should run daemon")
	}
	if isCLIInvocation([]string{"fjordd"}) {
		t.Fatal("bare fjordd should run daemon")
	}
}

func TestCLILifecycleAndInstallOverUnixSocket(t *testing.T) {
	sockPath, srv, be := setupCLITestServer(t)

	// Pre-create the per-stack directory so MkdirOwned adopts it without
	// requiring root to lchown(1000, 1000) in unprivileged unit tests.
	if err := os.MkdirAll(filepath.Join(srv.fjordRoot, "containers", "radarr-movies", "config"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. Install radarr via CLI (`fjord install radarr --set MOVIES_PATH=/tmp/movies`)
	moviesDir := t.TempDir()
	code, out, errStr := runCLIWithSocket(t, sockPath, "", "install", "radarr",
		"--name", "Radarr Movies",
		"--set", "MOVIES_PATH="+moviesDir,
		"--set", "WEB_PORT=17878",
	)
	if code != 0 {
		t.Fatalf("install failed (code=%d): stdout=%q stderr=%q", code, out, errStr)
	}
	if !strings.Contains(out, "stayed up") {
		t.Errorf("install output missing stream text: %q", out)
	}

	// 2. `fjord ls` and `fjord ls --json`
	code, out, errStr = runCLIWithSocket(t, sockPath, "", "ls")
	if code != 0 {
		t.Fatalf("ls failed: %s", errStr)
	}
	if !strings.Contains(out, "radarr-movies") || !strings.Contains(out, "Radarr Movies") {
		t.Errorf("ls table missing radarr-movies: %q", out)
	}
	code, out, errStr = runCLIWithSocket(t, sockPath, "", "ls", "--json")
	if code != 0 {
		t.Fatalf("ls --json failed: %s", errStr)
	}
	var list []stackWithStatus
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 {
		t.Fatalf("ls --json invalid: err=%v len=%d", err, len(list))
	}

	// 3. `fjord status` by display name and by id, plus `--json`
	code, out, errStr = runCLIWithSocket(t, sockPath, "", "status", "Radarr Movies")
	if code != 0 {
		t.Fatalf("status by display name failed: %s", errStr)
	}
	if !strings.Contains(out, "SERVICE") || !strings.Contains(out, "radarr") {
		t.Errorf("status output missing service table: %q", out)
	}

	// 4. `fjord up`, `fjord restart`, `fjord down`
	if code, _, errStr = runCLIWithSocket(t, sockPath, "", "up", "radarr-movies"); code != 0 {
		t.Fatalf("up failed: %s", errStr)
	}
	if code, _, errStr = runCLIWithSocket(t, sockPath, "", "restart", "radarr-movies"); code != 0 {
		t.Fatalf("restart failed: %s", errStr)
	}
	if code, _, errStr = runCLIWithSocket(t, sockPath, "", "down", "radarr-movies"); code != 0 {
		t.Fatalf("down failed: %s", errStr)
	}
	if be.downCalls != 1 || be.restCalls != 1 {
		t.Errorf("downCalls=%d restCalls=%d, want 1 each", be.downCalls, be.restCalls)
	}

	// 5. `fjord update radarr-movies radarr`
	if code, _, errStr = runCLIWithSocket(t, sockPath, "", "update", "radarr-movies", "radarr"); code != 0 {
		t.Fatalf("update failed: %s", errStr)
	}
	if len(be.updSvcs) != 1 || be.updSvcs[0] != "radarr" {
		t.Errorf("updSvcs = %v, want [radarr]", be.updSvcs)
	}

	// 6. Streamed `[error]` line causes non-zero exit code!
	be.mu.Lock()
	be.upStream = "starting...\n[error] container radarr exited with code 1\n"
	be.mu.Unlock()
	code, out, errStr = runCLIWithSocket(t, sockPath, "", "up", "radarr-movies")
	if code == 0 {
		t.Fatalf("expected non-zero exit on [error] stream line; stdout=%q stderr=%q", out, errStr)
	}
	if !strings.Contains(errStr, "container radarr exited with code 1") {
		t.Errorf("stderr = %q, want error message", errStr)
	}
	be.mu.Lock()
	be.upStream = ""
	be.mu.Unlock()

	// 7. `fjord logs` does NOT fail when app log output contains `[error]`
	code, out, errStr = runCLIWithSocket(t, sockPath, "", "logs", "--tail", "50", "radarr-movies", "radarr")
	if code != 0 {
		t.Fatalf("logs failed: %s", errStr)
	}
	if !strings.Contains(out, "[error] app log line") {
		t.Errorf("logs output = %q", out)
	}
	if len(be.logCtrs) != 1 || be.logCtrs[0] != "radarr-movies_radarr_1" {
		t.Errorf("logs container filter = %v, want [radarr-movies_radarr_1]", be.logCtrs)
	}

	// 8. `fjord rollback radarr-movies`
	if err := srv.manager.UpdateState("radarr-movies", func(st *stack.State) *stack.State {
		st.Rollback = map[string]stack.RollbackImage{
			"radarr": {
				Compose: "ghcr.io/daemonless/radarr:latest",
				Ref:     "ghcr.io/daemonless/radarr:latest",
				Digest:  "sha256:1111111111111111111111111111111111111111111111111111111111111111",
				At:      time.Now().UTC().Format(time.RFC3339),
			},
		}
		return st
	}); err != nil {
		t.Fatal(err)
	}
	code, _, errStr = runCLIWithSocket(t, sockPath, "", "rollback", "radarr-movies")
	if code != 0 {
		t.Fatalf("rollback failed: %s", errStr)
	}

	// 9. `fjord prune --all-images`
	code, out, errStr = runCLIWithSocket(t, sockPath, "", "prune", "--all-images")
	if code != 0 {
		t.Fatalf("prune failed: %s", errStr)
	}
	if !be.pruneOpts.AllImages || !strings.Contains(out, "1.2GB") {
		t.Errorf("prune: opts=%+v out=%q", be.pruneOpts, out)
	}

	// 10. `fjord exec radarr-movies` over WebSocket
	code, out, errStr = runCLIWithSocket(t, sockPath, "echo hi\n", "exec", "radarr-movies")
	if code != 0 {
		t.Fatalf("exec failed: %s", errStr)
	}
	if !strings.Contains(out, "hello-from-shell") {
		t.Errorf("exec stdout = %q, want hello-from-shell", out)
	}

	// 11. `fjord delete` prompts without --yes and aborts on 'n', then deletes with --yes
	be.mu.Lock()
	be.stopped = true
	be.mu.Unlock()
	code, _, _ = runCLIWithSocket(t, sockPath, "n\n", "delete", "radarr-movies")
	if code == 0 {
		t.Fatal("delete with 'n' should abort with non-zero exit")
	}
	code, out, errStr = runCLIWithSocket(t, sockPath, "", "delete", "--yes", "--purge-data", "radarr-movies")
	if code != 0 {
		t.Fatalf("delete --yes failed: %s", errStr)
	}
	if !strings.Contains(out, "Deleted radarr-movies") {
		t.Errorf("delete output = %q", out)
	}
}
