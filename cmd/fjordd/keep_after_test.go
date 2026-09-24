package main

import (
	"io"
	"strings"
	"testing"
)

// The pin note comes after the whole stream, and its work runs once, only
// when the stream has ended -- the addresses exist only then.
func TestThenReaderAppendsOnce(t *testing.T) {
	calls := 0
	r := &thenReader{r: strings.NewReader("up\n"), c: io.NopCloser(nil), after: func() string {
		calls++
		return "[fjord] kept app\n"
	}}
	b, err := io.ReadAll(iotest1(r))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "up\n[fjord] kept app\n" || calls != 1 {
		t.Errorf("got %q after %d calls", b, calls)
	}
}

// iotest1 reads a byte at a time, so the EOF lands on its own Read.
func iotest1(r io.Reader) io.Reader { return struct{ io.Reader }{oneByte{r}} }

type oneByte struct{ r io.Reader }

func (o oneByte) Read(p []byte) (int, error) { return o.r.Read(p[:min(1, len(p))]) }
