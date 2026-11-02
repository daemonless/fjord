package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

// The name's target is where Open goes. An app with its own address on a
// reachable network is named at that address and the port it listens on
// inside (the stashed mapping says which); one behind this host is named
// at the address the browser reached fjord at and the published port.
func TestInstallNameRequestFollowsOpen(t *testing.T) {
	lan := func(n string) bool { return n == "lan86" }
	own := &stack.Stack{Name: "craft-anything", Compose: "services:\n  craft-anything:\n    networks:\n      lan86:\n        ipv4_address: 192.168.86.14\n    x-fjord-published:\n      - \"${WEB_PORT}:80\"\nnetworks:\n  lan86:\n    external: true\nx-fjord:\n  web_port: \"${WEB_PORT}\"\n", Env: "WEB_PORT=8094\n"}
	st := engine.StackStatus{State: "running"}
	n, ok := installNameRequest(own, st, "craft-anything", "http://jupiter:3567", lan)
	if !ok || n.Address != "192.168.86.14" || n.Port != 80 || n.HTTPS || n.Label != "craft-anything" {
		t.Fatalf("own address: ok=%v %+v", ok, n)
	}
	behind := &stack.Stack{Name: "radarr", Compose: "services:\n  radarr:\n    ports:\n      - \"${WEB_PORT}:7878\"\nx-fjord:\n  web_port: \"${WEB_PORT}\"\n  web_https: true\n", Env: "WEB_PORT=7878\n"}
	n, ok = installNameRequest(behind, st, "movies", "https://192.168.4.103:3567", lan)
	if !ok || n.Address != "192.168.4.103" || n.Port != 7878 || !n.HTTPS || n.Label != "movies" {
		t.Fatalf("behind the host: ok=%v %+v", ok, n)
	}
	if _, ok := installNameRequest(&stack.Stack{Name: "db", Compose: "services:\n  db: {}\n"}, st, "db", "", lan); ok {
		t.Fatal("a stack with no page got a name request")
	}
}

// A provider that cannot reach the app yet is asked again, a few times,
// ten seconds apart: the stay-up check saw the process, a page can take
// longer. Any other refusal is final, and so is success.
func TestGiveNameRetriesWhileUnreachable(t *testing.T) {
	var probes, posts atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/probe", func(w http.ResponseWriter, r *http.Request) {
		n := probes.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"reachable": n >= 3, "https": false})
	})
	mux.HandleFunc("/api/v1/hosts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		posts.Add(1)
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		json.NewEncoder(w).Encode(map[string]any{"id": "h1", "name": in["name"], "ref": in["ref"], "target": in["target"]})
	})
	mux.HandleFunc("/api/v1/hosts/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(405) })
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	p := &provider{Stack: "rp", base: srv.URL, key: "k", pinPath: filepath.Join(t.TempDir(), "names-rp.json")}
	var slept []time.Duration
	sleep := func(d time.Duration) { slept = append(slept, d) }
	res := giveName(context.Background(), p, nameRequest{Stack: "app", Label: "app", Address: "192.168.86.14", Port: 80}, sleep)
	if res.Host == nil || res.Host.Name != "app" || posts.Load() != 1 {
		t.Fatalf("named after retries: %+v posts=%d", res, posts.Load())
	}
	if len(slept) != 2 || slept[0] != 10*time.Second {
		t.Fatalf("slept %v, want two ten-second waits", slept)
	}
	// Reachable at once: no wait at all.
	probes.Store(10)
	slept = nil
	if res := giveName(context.Background(), p, nameRequest{Stack: "app2", Label: "app2", Address: "192.168.86.15", Port: 80}, sleep); res.Host == nil || len(slept) != 0 {
		t.Fatalf("reachable at once: %+v slept %v", res, slept)
	}
	// Never reachable: six tries, then the refusal with the address tried.
	probes.Store(-100)
	slept = nil
	res = giveName(context.Background(), p, nameRequest{Stack: "app3", Label: "app3", Address: "192.168.86.16", Port: 80}, sleep)
	if res.Host != nil || len(slept) != 5 || res.Error == "" {
		t.Fatalf("unreachable: %+v slept %d", res, len(slept))
	}
}

// What the install's Output says, so it reads the same as the rest of it.
func TestNoNameLine(t *testing.T) {
	st := &stack.Stack{Name: "7", State: &stack.State{DisplayName: "Craft Anything"}}
	if got := noNameLine(st, "rp cannot reach 192.168.86.14:80"); got != "[fjord] Craft Anything has no name yet: rp cannot reach 192.168.86.14:80. Give it one from the menu beside Open.\n" {
		t.Fatalf("%q", got)
	}
	if got := displayName(&stack.Stack{Name: "7"}); got != "7" {
		t.Fatalf("no display name: %q", got)
	}
}
