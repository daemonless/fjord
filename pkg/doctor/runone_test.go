package doctor

import (
	"context"
	"testing"
)

func TestRunOneUnknownCheck(t *testing.T) {
	if _, ok := RunOne(context.Background(), Config{}, "no-such-check"); ok {
		t.Fatal("RunOne found a check that does not exist")
	}
}
