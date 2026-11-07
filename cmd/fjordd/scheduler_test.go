package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/catalog"
)

func TestCatalogAutoDueThrottlesDeadSourceRetries(t *testing.T) {
	dir := t.TempDir()
	cat := catalog.New(dir, []catalog.Source{
		{ID: "dead", Name: "Dead", URL: "https://invalid.example.test"},
	})
	s := &server{fjordRoot: dir, cat: cat}
	now := time.Now()

	// First check with an unfetched source is due immediately.
	if !s.catalogAutoDue(6*time.Hour, now) {
		t.Fatal("expected initial unfetched catalog check to be due")
	}

	// Simulate a failed automatic refresh just 1 minute ago.
	s.catalogAutoAt = now.Add(-time.Minute)
	if s.catalogAutoDue(6*time.Hour, now) {
		t.Fatal("expected dead source retry to back off instead of firing every minute")
	}

	// After catalogRetryBackoff (15m), retry is allowed.
	s.catalogAutoAt = now.Add(-16 * time.Minute)
	if !s.catalogAutoDue(6*time.Hour, now) {
		t.Fatal("expected retry to be due after backoff")
	}
}

func TestShutdownHTTPServersHandlesRegisteredServer(t *testing.T) {
	httpServersMu.Lock()
	orig := httpServers
	httpServers = nil
	httpServersMu.Unlock()
	t.Cleanup(func() {
		httpServersMu.Lock()
		httpServers = orig
		httpServersMu.Unlock()
	})

	srv := &http.Server{Addr: "127.0.0.1:0"}
	registerHTTPServer(srv)
	shutdownHTTPServers(time.Second)
}
