package main

import (
	"context"

	"github.com/daemonless/fjord/pkg/engine/podman"
	"github.com/daemonless/fjord/pkg/registry"
	"github.com/daemonless/fjord/pkg/sbom"
	"github.com/daemonless/fjord/pkg/stack"
	"github.com/daemonless/fjord/pkg/updates"
)

// localVersions is an engine that can read a pulled image's version label.
type localVersions interface {
	ImageVersion(ctx context.Context, ref string) (string, error)
}

// classified is what updating one service changes and how big a move it is.
type classified struct {
	Diff  sbom.Diff
	Class updates.Class
	Key   string // what the update installs, for the first-seen table
}

// classifyService compares the running image with what an update would
// install -- the tag's current image, or the newer version's when the
// service is behind by one -- using what each image says about itself in
// the registry. Nothing is pulled. The Changes panel and auto-update both
// judge an update by this.
func (s *server) classifyService(ctx context.Context, st *stack.Stack, sv updates.Service) (classified, error) {
	target := sv.Image
	up := updates.Check(ctx, s.backendFor(st), []updates.Service{sv}, s.schemeFor)
	if up.State == "upgrade" && up.NewTag != "" {
		target = registry.Repo(sv.Image) + ":" + up.NewTag
	}
	key := candidateKey(up.State, sv.Image, up.Latest, up.NewTag)
	newIndex, err := registry.Digest(ctx, target)
	if err != nil {
		return classified{Key: key}, err
	}
	newDoc, err := sbom.For(ctx, target, newIndex, platformOf(ctx, target, newIndex))
	if err != nil {
		return classified{Key: key}, err
	}
	// The running side is best effort: an old image can be gone from the
	// registry, and the answer is then the new side on its own.
	var oldDoc *sbom.Doc
	if sv.Running != "" {
		oldDoc, _ = sbom.For(ctx, sv.Image, sv.Running, platformOf(ctx, sv.Image, sv.Running))
	}
	diff := sbom.Compare(oldDoc, newDoc)
	// The versions the class is judged by: labels, else the SBOM's entry for
	// the app, else the tags a version bump moves between.
	repo := registry.Repo(sv.Image)
	from, to := oldDoc.AppVersion(repo), newDoc.AppVersion(repo)
	// Gone from the registry, the running image is still on this host, with
	// its labels: without them a nightly rebuild reads as "unknown", which
	// only the All policy takes.
	if from == "" && sv.Running != "" {
		if lv, ok := s.backendFor(st).(localVersions); ok {
			from, _ = lv.ImageVersion(ctx, repo+"@"+sv.Running)
		}
	}
	if from == "" && to == "" && up.State == "upgrade" {
		from, to = up.FromVersion, up.ToVersion
	}
	if diff.VersionFrom == "" {
		diff.VersionFrom = from
	}
	if diff.VersionTo == "" {
		diff.VersionTo = to
	}
	return classified{Diff: diff, Class: updates.Classify(from, to), Key: key}, nil
}

// podman is the engine whose images can be gone from the registry yet run.
var _ localVersions = (*podman.Backend)(nil)
