package main

import (
	"context"
	"errors"
	"testing"
)

// Only the stale-service failure asks doctor; any other exec error is shown
// as it is.
func TestExecHintOnlyForMissingFile(t *testing.T) {
	err := errors.New(`exec create: 409 Conflict: container state improper`)
	if h := execHint(context.Background(), err); h != "" {
		t.Fatalf("hint for an unrelated error: %q", h)
	}
}
