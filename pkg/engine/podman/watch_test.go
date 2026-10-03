package podman

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/stack"
)

// A fake libpod API: the stack has one container; inspect answers as told.
func fakeAPI(t *testing.T, inspect func(n int32) (int, string)) *Backend {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			io.WriteString(w, `[{"Id":"c1","Names":["/app_web_1"],"State":"running","Labels":{"io.podman.compose.service":"web","io.podman.compose.project":"app"}}]`)
		case strings.HasSuffix(r.URL.Path, "/containers/c1/json"):
			code, body := inspect(atomic.AddInt32(&calls, 1))
			if code == 0 { // stall past the client's patience
				time.Sleep(300 * time.Millisecond)
				return
			}
			w.WriteHeader(code)
			io.WriteString(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	client := &http.Client{Timeout: 100 * time.Millisecond, Transport: &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) { return u, nil },
	}}
	return &Backend{http: client}
}

const upBody = `{"RestartCount":0,"State":{"Running":true,"ExitCode":0,"StartedAt":"2026-10-02T10:00:00Z"}}`

func watch(t *testing.T, b *Backend) string {
	t.Helper()
	jailID = func(context.Context, string) string { return "" } // no app probe here
	t.Cleanup(func() { jailID = func(ctx context.Context, id string) string { return "" } })
	pr, pw := io.Pipe()
	done := make(chan string)
	go func() { out, _ := io.ReadAll(pr); done <- string(out) }()
	b.watchHealthy(context.Background(), pw, &stack.Stack{Name: "app"}, []string{"web"}, 3*time.Second)
	pw.Close()
	return <-done
}

// A timeout in the middle of the window is not a disappearance.
func TestWatchRidesOutASlowInspect(t *testing.T) {
	b := fakeAPI(t, func(n int32) (int, string) {
		if n == 2 {
			return 0, "" // one inspect times out
		}
		return 200, upBody
	})
	out := watch(t, b)
	if strings.Contains(out, "[error]") {
		t.Fatalf("a timeout was read as a failure:\n%s", out)
	}
	if !strings.Contains(out, "has stayed up") {
		t.Fatalf("want 'has stayed up':\n%s", out)
	}
}

// podman saying the container does not exist is a failure.
func TestWatchReportsAContainerThatIsGone(t *testing.T) {
	b := fakeAPI(t, func(n int32) (int, string) {
		if n >= 2 {
			return 404, `{"cause":"no such container"}`
		}
		return 200, upBody
	})
	if out := watch(t, b); !strings.Contains(out, "[error] web is gone since the update") {
		t.Fatalf("want gone:\n%s", out)
	}
}
