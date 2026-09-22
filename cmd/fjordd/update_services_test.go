package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/stack"
)

func TestRequestedServices(t *testing.T) {
	st := &stack.Stack{Name: "immich", Compose: "services:\n  immich-server:\n    image: a\n  database:\n    image: b\n"}
	req := func(body string) ([]string, error) {
		return requestedServices(httptest.NewRequest("POST", "/api/stacks/immich/update", strings.NewReader(body)), st)
	}
	// No body is the whole stack, as every update was before.
	if got, err := req(""); err != nil || got != nil {
		t.Errorf("empty body: %v, %v", got, err)
	}
	if got, err := req(`{"services":["database"]}`); err != nil || len(got) != 1 || got[0] != "database" {
		t.Errorf("database: %v, %v", got, err)
	}
	// A typo is refused up front, not after the pull when compose says so.
	if _, err := req(`{"services":["databse"]}`); err == nil {
		t.Error("unknown service accepted")
	}
	if _, err := req(`{"services":`); err == nil {
		t.Error("broken JSON accepted")
	}
}
