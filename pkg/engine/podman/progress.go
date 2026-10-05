package podman

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/registry"
)

// podman goes quiet for minutes in the middle of an action. A pull prints a
// "Copying blob" line for every layer in its first second (they download in
// parallel) and nothing more until the image is written -- on netlab 90 s
// for immich-ml, with one 52 s stretch on a single layer. podman-compose is
// as quiet while a container starts. The Output pane looked stuck.
//
// So fjord fills the silence with what it knows for certain: the image's
// size before a pull, and every quietEvery of silence a line saying what is
// still running and for how long.
var quietEvery = 15 * time.Second

// quietNotice writes "[fjord] still <what> · <elapsed>" into out each time
// nothing else has been written for quietEvery, until the returned stop.
func quietNotice(out *stampedWriter, what string) (stop func()) {
	start := time.Now()
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
			}
			if time.Since(out.lastWrite()) >= quietEvery {
				out.note(fmt.Sprintf("[fjord] still %s · %s\n", what, time.Since(start).Round(time.Second)))
			}
		}
	}()
	return func() { close(done) }
}

// runNoticed is runStreaming with quietNotice around the command.
func (b *Backend) runNoticed(ctx context.Context, w io.Writer, dir, what, name string, args ...string) error {
	out := &stampedWriter{w: w, last: time.Now(), lastCmd: time.Now()}
	fmt.Fprintf(out, "$ %s %s\n", name, strings.Join(args, " "))
	stop := quietNotice(out, what)
	defer stop()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

// pullNoticed pulls one image, saying first how much it is and then, through
// the silence, that it is still going.
func (b *Backend) pullNoticed(ctx context.Context, w io.Writer, dir, image string) error {
	what := "pulling " + image
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	size, layers, err := registry.ImageSize(sctx, image)
	cancel()
	if err == nil && layers > 0 {
		what = fmt.Sprintf("pulling %s (%s)", image, humanBytes(size))
		fmt.Fprintf(w, "[fjord] pulling %s: %s in %d layers. podman prints nothing while it downloads and writes them; fjord says every %s that it is still going.\n",
			image, humanBytes(size), layers, quietEvery)
	}
	return b.runNoticed(ctx, w, dir, what, "podman", "pull", image)
}

// humanBytes is a size the way the registry counts it (decimal units).
func humanBytes(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.0f MB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.0f kB", float64(n)/1e3)
	}
	return fmt.Sprintf("%d B", n)
}
