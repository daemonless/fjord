package podman

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/stack"
)

// A fake libpod API that lists the given containers for the stack, or fails.
func fakeList(t *testing.T, status int, body string) *Backend {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/containers/json") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return &Backend{http: &http.Client{Timeout: time.Second, Transport: &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) { return u, nil },
	}}}
}

// Updating tailscale while it was stopped brought it up. Whether a stack is
// stopped is decided by its containers, per service being updated; not being
// able to ask counts as running, so the update does what it always did.
func TestNotRunning(t *testing.T) {
	const web = `{"Names":["/app_web_1"],"State":"%s","Labels":{"io.podman.compose.service":"web","io.podman.compose.project":"app"}}`
	const db = `{"Names":["/app_db_1"],"State":"running","Labels":{"io.podman.compose.service":"db","io.podman.compose.project":"app"}}`
	s := &stack.Stack{Name: "app"}
	ctx := context.Background()

	cases := []struct {
		name     string
		status   int
		body     string
		services []string
		want     bool
	}{
		{"stopped in fjord: no containers", 200, `[]`, nil, true},
		{"exited container", 200, "[" + strings.Replace(web, "%s", "exited", 1) + "]", nil, true},
		{"running container", 200, "[" + strings.Replace(web, "%s", "running", 1) + "]", nil, false},
		{"updating a stopped service beside a running one", 200, "[" + strings.Replace(web, "%s", "exited", 1) + "," + db + "]", []string{"web"}, true},
		{"the whole stack, one service running", 200, "[" + strings.Replace(web, "%s", "exited", 1) + "," + db + "]", nil, false},
		{"podman cannot be asked", 500, `boom`, nil, false},
	}
	for _, c := range cases {
		b := fakeList(t, c.status, c.body)
		if got := b.notRunning(ctx, s, c.services); got != c.want {
			t.Errorf("%s: notRunning = %v, want %v", c.name, got, c.want)
		}
	}
}
