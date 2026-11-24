package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/registry"
	"github.com/daemonless/fjord/pkg/stack"
)

// Every service of a new stack is asked about, not only the main image; the
// tag a variable sets is the one asked; a pinned digest, and a registry that
// cannot answer, do not block.
func TestNoBuildForInstall(t *testing.T) {
	s := &server{}
	var asked []string
	s.platformsFn = func(_ context.Context, image, tag string) ([]string, error) {
		asked = append(asked, image+":"+tag)
		switch tag {
		case "other-arch":
			return []string{"freebsd/" + otherArch()}, nil
		case "flaky":
			return nil, errors.New("registry: 503")
		}
		return []string{registry.HostPlatform()}, nil
	}
	st := &stack.Stack{
		Compose: "services:\n" +
			"  app:\n    image: ghcr.io/x/app:${APP_TAG}\n" +
			"  db:\n    image: ghcr.io/x/db:other-arch\n" +
			"  cache:\n    image: ghcr.io/x/cache@sha256:abc\n",
		Env: "APP_TAG=1.2.3\n",
	}
	why := s.noBuildForInstall(context.Background(), st)
	if !strings.HasPrefix(why, "db: ") || !strings.Contains(why, "no build for this host") {
		t.Errorf("refusal = %q", why)
	}
	if strings.Join(asked, " ") != "ghcr.io/x/app:1.2.3 ghcr.io/x/db:other-arch" {
		t.Errorf("asked %v", asked)
	}

	st.Compose = "services:\n  app:\n    image: ghcr.io/x/app:flaky\n  web:\n    image: ghcr.io/x/web\n"
	asked = nil
	if why := s.noBuildForInstall(context.Background(), st); why != "" {
		t.Errorf("registry error or a host build must not block: %q", why)
	}
	if strings.Join(asked, " ") != "ghcr.io/x/app:flaky ghcr.io/x/web:latest" {
		t.Errorf("asked %v", asked)
	}
}

func TestInstallTag(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/x/app:1.2":        "1.2",
		"ghcr.io/x/app":            "latest",
		"localhost:5000/x/app":     "latest",
		"localhost:5000/x/app:pkg": "pkg",
		"alpine":                   "latest",
	} {
		if got := installTag(in); got != want {
			t.Errorf("installTag(%q) = %q, want %q", in, got, want)
		}
	}
}

// builtHere answers that every tag has a build for this host, so a test
// that installs never asks a real registry.
func builtHere(context.Context, string, string) ([]string, error) {
	return []string{registry.HostPlatform()}, nil
}
