package podman

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A silent command gets "still ..." lines, and they do not count as the
// command's output: the stuck-dependency watch must still see it silent.
func TestQuietNotice(t *testing.T) {
	defer func(d time.Duration) { quietEvery = d }(quietEvery)
	quietEvery = 1 * time.Second
	buf := &syncBuf{}
	start := time.Now().Add(-time.Hour)
	out := &stampedWriter{w: buf, last: start, lastCmd: start}
	stop := quietNotice(out, "pulling ghcr.io/x/y (1.4 GB)")
	time.Sleep(2500 * time.Millisecond)
	stop()
	got := buf.String()
	if n := strings.Count(got, "[fjord] still pulling ghcr.io/x/y (1.4 GB) · "); n < 1 || n > 3 {
		t.Fatalf("%d notices in 2.5 s of silence:\n%s", n, got)
	}
	if !out.lastCmdWrite().Equal(start) {
		t.Fatal("a notice counted as the command's output")
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{1394899539: "1.4 GB", 72549847: "73 MB", 4200: "4 kB", 12: "12 B"} {
		if got := humanBytes(n); got != want {
			t.Errorf("%d: got %q, want %q", n, got, want)
		}
	}
}
