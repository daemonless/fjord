package main

import (
	"context"
	"strings"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/registry"
	"github.com/daemonless/fjord/pkg/stack"
)

// noBuildForInstall is the refusal when one of a new stack's images has no
// build for this host, asked of the registry before anything is written: the
// wizard checks only the main image, and only in the browser. An image
// pinned to a digest, one whose reference is still a variable, or one the
// registry cannot answer for goes through; the engine checks the pulled
// image again before it starts.
func (s *server) noBuildForInstall(ctx context.Context, st *stack.Stack) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	asked := map[string]bool{}
	for _, svc := range composepkg.ParseServices(st.Compose, st.EnvMap()) {
		img := svc.Image
		if img == "" || strings.Contains(img, "@") || strings.Contains(img, "${") || asked[img] {
			continue
		}
		asked[img] = true
		if why := s.noBuildHere(ctx, registry.Repo(img), installTag(img)); why != "" {
			return svc.Name + ": " + why
		}
	}
	return ""
}

// installTag is an image reference's tag, "latest" when it names none.
func installTag(image string) string {
	slash := strings.LastIndex(image, "/")
	if colon := strings.LastIndex(image, ":"); colon > slash {
		return image[colon+1:]
	}
	return "latest"
}
