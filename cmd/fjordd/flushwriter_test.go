package main

import (
	"errors"
	"net/http"
	"testing"
)

// goneClient is a closed tab: every write fails.
type goneClient struct{ http.ResponseWriter }

func (goneClient) Header() http.Header       { return http.Header{} }
func (goneClient) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
func (goneClient) WriteHeader(int)           {}

// A closed tab must not stall pkg: writes still succeed.
func TestFlushWriterOutlivesTheClient(t *testing.T) {
	w := flushWriter{goneClient{}}
	n, err := w.Write([]byte("Installing podman...\n"))
	if err != nil || n != len("Installing podman...\n") {
		t.Fatalf("Write = %d, %v; want all bytes accepted and no error", n, err)
	}
}
