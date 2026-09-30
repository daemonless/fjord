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
