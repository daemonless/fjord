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

// Digests podman reports without a repository ("sha256:...", no "@") were
// dropped, and hasBytes then read known[0] of an empty list: a panic in the
// update check (review 4.3). A bare digest counts, and an empty list is "no".
func TestHasBytesWithBareOrNoDigests(t *testing.T) {
	old := registryPlatform
	t.Cleanup(func() { registryPlatform = old })
	registryPlatform = func(_ context.Context, _, index string) (string, error) { return "sha256:plat", nil }
	if hasBytes(context.Background(), "x", "sha256:reg", nil) {
		t.Error("nothing known, yet has the bytes")
	}
	if got := localDigestsOf([]string{"sha256:abc", "repo@sha256:def"}); len(got) != 2 || got[0] != "sha256:abc" || got[1] != "sha256:def" {
		t.Errorf("local digests %v", got)
	}
}

// Docker Hub's postgres, as registry.Trains files it: every bare version in
// one train, pre-releases and majors mixed. A stack on 17 was moved to 19rc1,
// which will not start on a 17 data directory.
func TestUpgradeOffersOnlySafeSteps(t *testing.T) {
	v := func(pairs ...string) []registry.Version {
		var out []registry.Version
		for i := 0; i < len(pairs); i += 2 {
			out = append(out, registry.Version{Version: pairs[i], Tag: pairs[i+1]})
		}
		return out
	}
	fakeRegistry(t, nil, map[string]map[string][]registry.Version{
		"docker.io/library/postgres": {
			"latest": v("19rc1", "19rc1", "19beta2", "19beta2", "18beta1", "18beta1", "18.1", "18.1", "18", "18",
				"17rc1", "17rc1", "17beta2", "17beta2", "17.7", "17.7", "17.6", "17.6", "17", "17"),
			"alpine": v("19rc1", "19rc1-alpine", "18.1", "18.1-alpine", "18", "18-alpine",
				"17.7", "17.7-alpine", "17.6", "17.6-alpine", "17", "17-alpine"),
		},
		"ghcr.io/x/calver": {"latest": v("2026.12.0.dev2026103102", "2026.12.0.dev2026103102", "2025.1.1", "2025.1.1",
			"2025.1.0.dev2024122001", "2025.1.0.dev2024122001", "2025.1.0", "2025.1.0", "2024.10.2", "2024.10.2")},
		"docker.io/library/python":  {"latest": v("3.16.0a1", "3.16.0a1", "3.12.9", "3.12.9", "3.12.1", "3.12.1")},
		"docker.io/library/mariadb": {"latest": v("11.8.9-ubi9", "11.8.9-ubi9", "11.8.9", "11.8.9", "11.4.2", "11.4.2")},
		"lscr.io/x/babybuddy":       {"latest": v("2.11.0", "2.11.0", "2.10.1", "2.10.1", "1.16.0", "1.16.0", "1.9.0", "1.9.0")},
		"ghcr.io/x/nextcloud":       {"latest": v("32.0.1", "32.0.1", "31.0.9", "31.0.9", "31.0.0", "31.0.0", "30.0.17", "30.0.17")},
		"ghcr.io/x/vprefixed":       {"latest": v("v2.0.0", "v2.0.0", "v1.2.0", "v1.2.0", "v1.1.0", "v1.1.0")},
	})
	for _, c := range []struct{ repo, tag, want string }{
		{"docker.io/library/postgres", "17", ""},        // rolling: follows 17 already
		{"docker.io/library/postgres", "17-alpine", ""}, // the same, on alpine
		{"docker.io/library/postgres", "17.6", "17.7"},  // newest stable 17, not 17rc1 or 18.1
		{"docker.io/library/postgres", "17.6-alpine", "17.7-alpine"},
		{"docker.io/library/postgres", "17.7", ""},         // newest 17 already
		{"docker.io/library/postgres", "18.1", ""},         // 19 is a new major, and only an rc
		{"docker.io/library/postgres", "19beta2", "19rc1"}, // already on a pre-release: the next one
		{"ghcr.io/x/calver", "2024.10.2", "2025.1.1"},      // a new year is not a new major
		{"ghcr.io/x/calver", "2025.1.0", "2025.1.1"},       // its .dev builds don't make it rolling
		{"docker.io/library/python", "3.12.1", "3.12.9"},   // not 3.16.0a1
		{"docker.io/library/mariadb", "11.4.2", "11.8.9"},  // not the ubi9 flavour
		{"lscr.io/x/babybuddy", "1.9.0", "2.11.0"},         // an app's next major is offered
		{"ghcr.io/x/nextcloud", "30.0.17", "31.0.9"},       // one major at a time, not 32
		{"ghcr.io/x/vprefixed", "v1.1.0", "v2.0.0"},        // "v" is not a major of its own
	} {
		up, ok := newerVersion(context.Background(), c.repo, c.tag, nil)
		if got := up.NewTag; got != c.want || ok != (c.want != "") {
			t.Errorf("%s:%s offered %q (%v), want %q", c.repo, c.tag, got, ok, c.want)
		}
	}
}

func TestDataTiedToMajor(t *testing.T) {
	for repo, want := range map[string]bool{
		"postgres":                         true,
		"docker.io/library/postgres":       true,
		"ghcr.io/daemonless/postgres":      true,
		"ghcr.io/daemonless/cnpg-postgres": true,
		"ghcr.io/immich-app/postgres":      true,
		"pgvector/pgvector":                true,
		"postgis/postgis":                  true,
		"timescale/timescaledb-ha":         true,
		"bitnami/postgresql":               true,
		"docker.io/library/mongo":          true,
		"mongodb/mongodb-community-server": true,
		"elasticsearch":                    true,
		"opensearchproject/opensearch":     true,
		"docker.io/library/mariadb":        false,
		"docker.io/library/redis":          false,
		"lscr.io/linuxserver/babybuddy":    false,
		"ghcr.io/x/postgres-exporter":      false,
		"ghcr.io/mealie-recipes/mealie":    false,
	} {
		if got := DataTiedToMajor(repo); got != want {
			t.Errorf("DataTiedToMajor(%q) = %v, want %v", repo, got, want)
		}
	}
}
