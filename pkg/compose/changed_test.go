package compose

import (
	"slices"
	"testing"
)

const twoServices = `services:
  web:
    image: ghcr.io/daemonless/zensical:latest
    ports:
      - "${WEB_PORT}:8000"
  db:
    image: ghcr.io/daemonless/postgres:17
    env_file: .env
`

func TestChangedServices(t *testing.T) {
	env := map[string]string{"WEB_PORT": "8000", "POSTGRES_PASSWORD": "a"}
	same := func(got []string) bool { return len(got) == 0 }

	if got := ChangedServices(twoServices, env, twoServices, env); !same(got) {
		t.Errorf("nothing changed, got %v", got)
	}
	// Comments and key order are not changes.
	reordered := "# a comment\nservices:\n  db:\n    env_file: .env\n    image: ghcr.io/daemonless/postgres:17\n  web:\n    ports:\n      - \"${WEB_PORT}:8000\"\n    image: ghcr.io/daemonless/zensical:latest\n"
	if got := ChangedServices(twoServices, env, reordered, env); !same(got) {
		t.Errorf("comment/order only, got %v", got)
	}
	// A variable the compose uses: only web changes... and db, whose env_file
	// is the .env.
	port := map[string]string{"WEB_PORT": "8001", "POSTGRES_PASSWORD": "a"}
	if got := ChangedServices(twoServices, env, twoServices, port); !slices.Equal(got, []string{"db", "web"}) {
		t.Errorf("WEB_PORT changed, got %v", got)
	}
	// A variable only the env_file carries: db alone.
	pw := map[string]string{"WEB_PORT": "8000", "POSTGRES_PASSWORD": "b"}
	if got := ChangedServices(twoServices, env, twoServices, pw); !slices.Equal(got, []string{"db"}) {
		t.Errorf("password changed, got %v", got)
	}
	// A new service counts; a removed one does not.
	added := twoServices + "  redis:\n    image: ghcr.io/daemonless/redis:latest\n"
	if got := ChangedServices(twoServices, env, added, env); !slices.Equal(got, []string{"redis"}) {
		t.Errorf("added redis, got %v", got)
	}
	if got := ChangedServices(added, env, twoServices, env); !same(got) {
		t.Errorf("removed redis, got %v", got)
	}
}

const anchoredServices = `x-common: &common
  restart: unless-stopped
  env_file: .env
  volumes:
    - /data:/data

x-ports: &webports
  ports:
    - "${WEB_PORT}:8000"

services:
  web:
    <<: [*common, *webports]
    image: ghcr.io/daemonless/zensical:latest
  db:
    <<: *common
    image: ghcr.io/daemonless/postgres:17
`

func TestChangedServicesWithAnchorsAndMerges(t *testing.T) {
	env := map[string]string{"WEB_PORT": "8000", "POSTGRES_PASSWORD": "a"}

	if got := ChangedServices(anchoredServices, env, anchoredServices, env); len(got) != 0 {
		t.Fatalf("nothing changed, got %v", got)
	}

	// Editing a service that uses <<: *common must be detected.
	editedWeb := `x-common: &common
  restart: unless-stopped
  env_file: .env
  volumes:
    - /data:/data

x-ports: &webports
  ports:
    - "${WEB_PORT}:8000"

services:
  web:
    <<: [*common, *webports]
    image: ghcr.io/daemonless/zensical:pkg
  db:
    <<: *common
    image: ghcr.io/daemonless/postgres:17
`
	if got := ChangedServices(anchoredServices, env, editedWeb, env); !slices.Equal(got, []string{"web"}) {
		t.Errorf("edited web image, got %v, want [web]", got)
	}

	// Editing the shared anchor marks every service that merges it as changed.
	editedAnchor := `x-common: &common
  restart: always
  env_file: .env
  volumes:
    - /data:/data

x-ports: &webports
  ports:
    - "${WEB_PORT}:8000"

services:
  web:
    <<: [*common, *webports]
    image: ghcr.io/daemonless/zensical:latest
  db:
    <<: *common
    image: ghcr.io/daemonless/postgres:17
`
	if got := ChangedServices(anchoredServices, env, editedAnchor, env); !slices.Equal(got, []string{"db", "web"}) {
		t.Errorf("edited anchor, got %v, want [db web]", got)
	}

	// Changing .env affects services that inherit env_file: .env via <<: *common.
	env2 := map[string]string{"WEB_PORT": "8000", "POSTGRES_PASSWORD": "b"}
	if got := ChangedServices(anchoredServices, env, anchoredServices, env2); !slices.Equal(got, []string{"db", "web"}) {
		t.Errorf("env_file via anchor, got %v, want [db web]", got)
	}
}

func TestAnchoredServiceInspection(t *testing.T) {
	const composeYAML = `x-common: &common
  restart: unless-stopped
  network_mode: host
  ports:
    - "8080:80"
  volumes:
    - "/containers/app:/config:ro"
  environment:
    TZ: UTC

services:
  app:
    <<: *common
    image: ghcr.io/daemonless/radarr:latest
`
	svcs := ParseServices(composeYAML, nil)
	if len(svcs) != 1 {
		t.Fatalf("ParseServices: got %d services, want 1", len(svcs))
	}
	s := svcs[0]
	if !s.NetworkHost {
		t.Errorf("inherited network_mode: host not detected")
	}
	if len(s.Ports) != 1 || s.Ports[0].Host != 8080 || s.Ports[0].Container != 80 {
		t.Errorf("inherited ports = %+v, want 8080:80", s.Ports)
	}
	if len(s.Volumes) != 1 || s.Volumes[0].Source != "/containers/app" || !s.Volumes[0].ReadOnly {
		t.Errorf("inherited volumes = %+v", s.Volumes)
	}
	if s.Env["TZ"] != "UTC" {
		t.Errorf("inherited environment TZ = %q, want UTC", s.Env["TZ"])
	}
	if ports := PublishedPorts(composeYAML, nil); len(ports) != 1 || ports[0].Host != 8080 {
		t.Errorf("PublishedPorts = %+v, want [8080/tcp]", ports)
	}
	if binds := BindMounts(composeYAML, nil); len(binds) != 1 || binds[0].Source != "/containers/app" {
		t.Errorf("BindMounts = %+v", binds)
	}
	if mounts := Mounts(composeYAML, nil); len(mounts) != 1 || mounts[0].Dest != "/config" {
		t.Errorf("Mounts = %+v", mounts)
	}
	if mode := NetworkMode(composeYAML); mode != Host {
		t.Errorf("NetworkMode = %q, want %q", mode, Host)
	}
	if restarts := ServiceRestarts(composeYAML); restarts["app"] != RestartUnlessStopped {
		t.Errorf("ServiceRestarts = %+v", restarts)
	}
}
