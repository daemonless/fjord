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
)

// The running image's version comes from its own labels on this host when
// the registry no longer has it; an image not pulled is "", not an error.
func TestImageVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "top-level"):
			io.WriteString(w, `{"Labels":{"org.opencontainers.image.version":"2.1.3"}}`)
		case strings.Contains(r.URL.Path, "config"):
			io.WriteString(w, `{"Config":{"Labels":{"org.opencontainers.image.version":"1.4.0_1"}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	b := &Backend{http: &http.Client{Timeout: time.Second, Transport: &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) { return u, nil },
	}}}
	ctx := context.Background()
	for ref, want := range map[string]string{"top-level@sha256:a": "2.1.3", "config@sha256:b": "1.4.0_1", "gone@sha256:c": ""} {
		if got, err := b.ImageVersion(ctx, ref); err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", ref, got, err, want)
		}
	}
}
