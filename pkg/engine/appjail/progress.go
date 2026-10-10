package appjail

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/daemonless/fjord/pkg/stack"
)

// Director builds a project's jails one at a time, where podman-compose
// starts its containers together, so a several-service stack sits a while on
// a short Output panel. `up` says how many it will build and counts them off.

// directorUpStep runs `up`, preceded by what it will do and with its output
// numbered. Counted when the step runs, so after an update's teardown.
func directorUpStep(ctx context.Context, s *stack.Stack) func(io.Writer) error {
	return func(w io.Writer) error {
		var build, start int
		for _, sj := range directorServices(directorFile(s), s) {
			switch {
			case exec.CommandContext(ctx, "appjail", "jail", "get", "--", sj.jail, "name").Run() != nil:
				build++
			case exec.CommandContext(ctx, "appjail", "status", "-q", sj.jail).Run() != nil:
				start++
			}
		}
		if build > 0 {
			fmt.Fprintf(w, "[fjord] %s\n", upSummary(build, start))
		}
		return runDirector(ctx, newProgressWriter(w, build+start), s.Dir, false, "up")
	}
}

// upSummary words what `up` is about to do.
func upSummary(build, start int) string {
	what := plural(build, "service") + " to build"
	if start > 0 {
		what += fmt.Sprintf(", %d to start", start)
	}
	return "AppJail builds services one at a time: " + what + ". This takes a few moments."
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// progressWriter numbers director's per-service lines:
//
//	Creating web (myapp_web) ... Done.  ->  [2/7] Creating web (myapp_web) ... Done.
//	Starting web (myapp_web) ... Done.  ->  [2/7] Starting web (myapp_web) ... Done.
//
// Director prints "Creating ... " and flushes, then "Done." when the jail is
// built, so the start of a line is held only until its service name is known;
// the rest passes straight through and the wait stays visible.
type progressWriter struct {
	w     io.Writer
	total int
	n     int
	seen  map[string]int
	line  []byte // start of the current line, held until it can be numbered
	mid   bool   // the current line's start has already been written
}

func newProgressWriter(w io.Writer, total int) *progressWriter {
	return &progressWriter{w: w, total: total, seen: map[string]int{}}
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		chunk := b
		if i >= 0 {
			chunk = b[:i+1]
		}
		b = b[len(chunk):]
		if p.mid {
			if _, err := p.w.Write(chunk); err != nil {
				return n, err
			}
			p.mid = chunk[len(chunk)-1] != '\n'
			continue
		}
		p.line = append(p.line, chunk...)
		svc, decided := serviceLine(p.line)
		if !decided {
			continue
		}
		if svc != "" {
			k, ok := p.seen[svc]
			if !ok {
				p.n++
				k = p.n
				p.seen[svc] = k
			}
			// Director also rebuilds a running jail whose Makejail changed,
			// which the count up front cannot see.
			if p.total < p.n {
				p.total = p.n
			}
			fmt.Fprintf(p.w, "[%d/%d] ", k, p.total)
		}
		if _, err := p.w.Write(p.line); err != nil {
			return n, err
		}
		p.mid = p.line[len(p.line)-1] != '\n'
		p.line = p.line[:0]
	}
	return n, nil
}

// serviceLine reads the start of a director line: the service of a
// "Creating <svc> (<jail>) ..." or "Starting <svc> (<jail>) ..." line, "" for
// any other line, and decided=false while too little has arrived to tell.
// "Starting Director (project:x) ..." opens every run and is not a service.
func serviceLine(line []byte) (svc string, decided bool) {
	ended := bytes.HasSuffix(line, []byte("\n"))
	for _, kw := range []string{"Creating ", "Starting "} {
		if len(line) < len(kw) {
			if !ended && bytes.HasPrefix([]byte(kw), line) {
				return "", false
			}
			continue
		}
		if !bytes.HasPrefix(line, []byte(kw)) {
			continue
		}
		rest := line[len(kw):]
		sp := bytes.Index(rest, []byte(" ("))
		if sp < 0 {
			return "", ended
		}
		tail, pj := rest[sp:], []byte(" (project:")
		m := min(len(tail), len(pj))
		if !bytes.Equal(tail[:m], pj[:m]) {
			return string(rest[:sp]), true
		}
		return "", ended || m == len(pj)
	}
	return "", true
}
