package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/registry"
	"github.com/daemonless/fjord/pkg/stack"
)

// A version change onto a tag with no build for this host is refused with a
// reason; one the registry cannot answer for goes through.
func TestSetTagRefusesNoBuildHere(t *testing.T) {
	s := &server{manager: stack.NewManager(t.TempDir())}
	if err := s.manager.Save(&stack.Stack{Name: "app", Compose: "services:\n  web:\n    image: ghcr.io/x/web:latest\n"}); err != nil {
		t.Fatal(err)
	}
	asked := map[string]bool{}
	s.platformsFn = func(_ context.Context, image, tag string) ([]string, error) {
		asked[image+":"+tag] = true
		switch tag {
		case "amd64-only":
			return []string{"freebsd/" + otherArch()}, nil
		case "flaky":
			return nil, errors.New("registry: 503")
		}
		return []string{registry.HostPlatform()}, nil
	}
	post := func(tag string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/stacks/app/set-tag", bytes.NewBufferString(`{"tag":"`+tag+`"}`))
		s.stackSetTag(w, r, "app")
		return w
	}
	if w := post("amd64-only"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "no build for this host") {
		t.Errorf("wrong arch: %d %q", w.Code, w.Body.String())
	}
	if !asked["ghcr.io/x/web:amd64-only"] {
		t.Errorf("the registry was asked about %v, not the image's repo", asked)
	}
	if w := post("flaky"); w.Code != http.StatusOK {
		t.Errorf("registry error must not block: %d %q", w.Code, w.Body.String())
	}
	if w := post("ok"); w.Code != http.StatusOK {
		t.Errorf("a tag built for this host: %d %q", w.Code, w.Body.String())
	}
}

func otherArch() string {
	if strings.HasSuffix(registry.HostPlatform(), "/arm64") {
		return "amd64"
	}
	return "arm64"
}

// A version change on an image whose tag is a variable goes into .env; the
// compose keeps the reference as written. Cutting the compose at the last
// colon wrote "${IMMICH_TAG:3.2.2" (immich on netlab, 2026-09-30).
func TestSetTagOnVariableTag(t *testing.T) {
	s := &server{manager: stack.NewManager(t.TempDir())}
	s.platformsFn = func(context.Context, string, string) ([]string, error) { return []string{registry.HostPlatform()}, nil }
	compose := "services:\n  immich-server:\n    image: ghcr.io/x/immich-server:${IMMICH_TAG:-latest}\n  database:\n    image: ghcr.io/x/immich-postgres:latest\n"
	if err := s.manager.Save(&stack.Stack{Name: "immich", Compose: compose, Env: "IMMICH_TAG=latest\nTZ=UTC\n"}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.stackSetTag(w, httptest.NewRequest(http.MethodPost, "/api/stacks/immich/set-tag", bytes.NewBufferString(`{"tag":"3.2.2","service":"immich-server"}`)), "immich")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	st, _ := s.manager.Get("immich")
	if st.Compose != compose {
		t.Errorf("compose changed:\n%s", st.Compose)
	}
	if st.Env != "IMMICH_TAG=3.2.2\nTZ=UTC\n" {
		t.Errorf("env = %q", st.Env)
	}
	// A literal tag still changes in the compose, and .env is left alone.
	w = httptest.NewRecorder()
	s.stackSetTag(w, httptest.NewRequest(http.MethodPost, "/api/stacks/immich/set-tag", bytes.NewBufferString(`{"tag":"17","service":"database"}`)), "immich")
	st, _ = s.manager.Get("immich")
	if w.Code != http.StatusOK || !strings.Contains(st.Compose, "immich-postgres:17") || st.Env != "IMMICH_TAG=3.2.2\nTZ=UTC\n" {
		t.Errorf("%d: compose:\n%s\nenv %q", w.Code, st.Compose, st.Env)
	}
}

func TestUpsertEnv(t *testing.T) {
	if got := upsertEnv("A=1\nTAG=latest\n", "TAG", "3.2.2"); got != "A=1\nTAG=3.2.2\n" {
		t.Errorf("replace: %q", got)
	}
	if got := upsertEnv("A=1", "TAG", "3.2.2"); got != "A=1\nTAG=3.2.2\n" {
		t.Errorf("append without newline: %q", got)
	}
	if got := upsertEnv("", "TAG", "x"); got != "TAG=x\n" {
		t.Errorf("empty: %q", got)
	}
}
