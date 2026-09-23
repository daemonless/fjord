package updates

import (
	"context"
	"errors"
	"testing"

	"github.com/daemonless/fjord/pkg/registry"
)

// platforms maps an index digest to this host's manifest inside it.
var platforms = map[string]string{}

// localTags is the local image store: ref -> RepoDigests.
type localTags map[string][]string

func (l localTags) ImageRepoDigests(_ context.Context, ref string) ([]string, error) {
	return l[ref], nil
}

// fakeRegistry answers digest lookups from a map; a missing ref is an error,
// as an unreachable registry would be. Trains come from a second map.
func fakeRegistry(t *testing.T, digests map[string]string, trains map[string]map[string][]registry.Version) {
	t.Helper()
	oldD, oldP, oldT := registryDigest, registryPlatform, registryTrains
	t.Cleanup(func() { registryDigest, registryPlatform, registryTrains = oldD, oldP, oldT })
	registryPlatform = func(_ context.Context, _, index string) (string, error) {
		if p, ok := platforms[index]; ok {
			return p, nil
		}
		return "", nil // a plain manifest, not an index
	}
	registryDigest = func(_ context.Context, image string) (string, error) {
		if d, ok := digests[image]; ok {
			return d, nil
		}
		return "", errors.New("unreachable")
	}
	registryTrains = func(_ context.Context, repo string, _ *registry.Scheme) (map[string][]registry.Version, error) {
		return trains[repo], nil
	}
}

func byService(st Status) map[string]ServiceStatus {
	m := map[string]ServiceStatus{}
	for _, s := range st.Services {
		m[s.Service] = s
	}
	return m
}

// The bug this exists for: something else pulled the tag (another stack
// sharing redis, the maintenance playbook), so the LOCAL tag matches the
// registry while the container still runs the old bytes. Judged by the local
// tag that read "current" and nothing would ever update it.
func TestRunningDigestBeatsLocalTag(t *testing.T) {
	fakeRegistry(t, map[string]string{"ghcr.io/x/redis:latest": "sha256:new"}, nil)
	local := localTags{"ghcr.io/x/redis:latest": {"ghcr.io/x/redis@sha256:new"}}

	st := Check(context.Background(), local, []Service{
		{Name: "redis", Image: "ghcr.io/x/redis:latest", Running: "sha256:old"},
	}, nil)
	if st.State != "available" {
		t.Fatalf("state = %q, want available: the container runs sha256:old", st.State)
	}
	if s := st.Services[0]; s.Running != "sha256:old" || s.Latest != "sha256:new" {
		t.Errorf("service = %+v, want running sha256:old, latest sha256:new", s)
	}

	// No container (or an engine that cannot say): the local tag is all
	// there is, and it matches.
	st = Check(context.Background(), local, []Service{{Name: "redis", Image: "ghcr.io/x/redis:latest"}}, nil)
	if st.State != "current" {
		t.Errorf("without a running digest: state = %q, want current", st.State)
	}
}

func TestRunningDigestCurrent(t *testing.T) {
	fakeRegistry(t, map[string]string{"ghcr.io/x/app:latest": "sha256:a"}, nil)
	st := Check(context.Background(), localTags{}, []Service{
		{Name: "app", Image: "ghcr.io/x/app:latest", Running: "sha256:a"},
	}, nil)
	if st.State != "current" {
		t.Errorf("state = %q, want current", st.State)
	}
}

// immich's shape: four services, one drifted, one with a newer version. The
// badge takes the drift (Update applies it), but both must be reported --
// before, the first "available" returned and the upgrade was never seen.
func TestEveryServiceReported(t *testing.T) {
	fakeRegistry(t,
		map[string]string{
			"ghcr.io/x/server:release": "sha256:s2",
			"ghcr.io/x/ml:release":     "sha256:m1",
			"ghcr.io/x/redis:latest":   "sha256:r1",
		},
		map[string]map[string][]registry.Version{
			"ghcr.io/x/postgres": {"16": {{Version: "16.5", Tag: "16.5-pkg"}, {Version: "16.4", Tag: "16.4-pkg"}}},
		})
	st := Check(context.Background(), localTags{}, []Service{
		{Name: "immich-server", Image: "ghcr.io/x/server:release", Running: "sha256:s1"},
		{Name: "immich-machine-learning", Image: "ghcr.io/x/ml:release", Running: "sha256:m1"},
		{Name: "redis", Image: "ghcr.io/x/redis:latest", Running: "sha256:r1"},
		{Name: "database", Image: "ghcr.io/x/postgres:16.4-pkg"},
	}, nil)

	if st.State != "available" || st.Latest != "sha256:s2" {
		t.Errorf("summary = %s latest %s, want available sha256:s2", st.State, st.Latest)
	}
	got := byService(st)
	want := map[string]string{
		"immich-server":           "available",
		"immich-machine-learning": "current",
		"redis":                   "current",
		"database":                "upgrade",
	}
	for svc, state := range want {
		if got[svc].State != state {
			t.Errorf("%s: state = %q, want %q", svc, got[svc].State, state)
		}
	}
	if db := got["database"]; db.FromVersion != "16.4" || db.ToVersion != "16.5" || db.NewTag != "16.5-pkg" {
		t.Errorf("database = %+v, want 16.4 -> 16.5 (16.5-pkg)", db)
	}
}

// One registry that cannot be reached makes THAT service unknown. It used to
// fail the whole check, so a stack with one private image never said anything
// about the other three.
func TestRegistryErrorIsPerService(t *testing.T) {
	fakeRegistry(t, map[string]string{"ghcr.io/x/app:latest": "sha256:a"}, nil)
	st := Check(context.Background(), localTags{}, []Service{
		{Name: "app", Image: "ghcr.io/x/app:latest", Running: "sha256:a"},
		{Name: "private", Image: "registry.lan/private:latest", Running: "sha256:p"},
	}, nil)
	got := byService(st)
	if got["app"].State != "current" {
		t.Errorf("app: state = %q, want current", got["app"].State)
	}
	if got["private"].State != "unknown" || got["private"].Detail == "" {
		t.Errorf("private = %+v, want unknown with a reason", got["private"])
	}
	if st.State != "unknown" {
		t.Errorf("summary = %q, want unknown", st.State)
	}
}

func TestPinned(t *testing.T) {
	fakeRegistry(t, map[string]string{"ghcr.io/x/app:latest": "sha256:a"}, nil)
	pinned := Service{Name: "db", Image: "ghcr.io/x/db:16@sha256:abc"}

	st := Check(context.Background(), localTags{}, []Service{pinned}, nil)
	if st.State != "pinned" {
		t.Errorf("all pinned: state = %q, want pinned", st.State)
	}
	st = Check(context.Background(), localTags{}, []Service{
		pinned, {Name: "app", Image: "ghcr.io/x/app:latest", Running: "sha256:a"},
	}, nil)
	if st.State != "current" {
		t.Errorf("pinned + current: state = %q, want current", st.State)
	}
}

func TestNoServices(t *testing.T) {
	if st := Check(context.Background(), localTags{}, nil, nil); st.State != "unknown" {
		t.Errorf("state = %q, want unknown", st.State)
	}
}

// Found on jupiter: smokeping, tautulli and unifi all read "available" while
// each ran exactly the image its tag pointed to. The index had been rebuilt
// for arm64 only; the amd64 manifest inside it was the one already running.
func TestOtherArchRebuildIsNotAnUpdate(t *testing.T) {
	fakeRegistry(t, map[string]string{"ghcr.io/x/app:latest": "sha256:index2"}, nil)
	platforms = map[string]string{"sha256:index2": "sha256:amd64-a"}
	t.Cleanup(func() { platforms = map[string]string{} })

	// Running was pulled as index1; its image also answers to its amd64
	// manifest, which index2 still names.
	st := Check(context.Background(), localTags{}, []Service{{
		Name: "app", Image: "ghcr.io/x/app:latest",
		Running: "sha256:index1", Known: []string{"sha256:index1", "sha256:amd64-a"},
	}}, nil)
	if st.State != "current" {
		t.Errorf("same amd64 bytes under a new index: state = %q, want current", st.State)
	}

	// The amd64 manifest really changed: that IS an update.
	platforms = map[string]string{"sha256:index2": "sha256:amd64-b"}
	st = Check(context.Background(), localTags{}, []Service{{
		Name: "app", Image: "ghcr.io/x/app:latest",
		Running: "sha256:index1", Known: []string{"sha256:index1", "sha256:amd64-a"},
	}}, nil)
	if st.State != "available" {
		t.Errorf("new amd64 bytes: state = %q, want available", st.State)
	}
}

// The same rebuild before anything re-pulled: the running image knows only
// the index it came from, so the host's manifest has to be read out of both.
func TestOtherArchRebuildNotYetPulled(t *testing.T) {
	fakeRegistry(t, map[string]string{"ghcr.io/x/app:latest": "sha256:index2"}, nil)
	platforms = map[string]string{"sha256:index1": "sha256:amd64-a", "sha256:index2": "sha256:amd64-a"}
	t.Cleanup(func() { platforms = map[string]string{} })

	svc := Service{Name: "app", Image: "ghcr.io/x/app:latest", Running: "sha256:index1", Known: []string{"sha256:index1"}}
	if st := Check(context.Background(), localTags{}, []Service{svc}, nil); st.State != "current" {
		t.Errorf("same amd64 bytes, not re-pulled: state = %q, want current", st.State)
	}
	platforms["sha256:index2"] = "sha256:amd64-b"
	if st := Check(context.Background(), localTags{}, []Service{svc}, nil); st.State != "available" {
		t.Errorf("new amd64 bytes, not re-pulled: state = %q, want available", st.State)
	}
}
