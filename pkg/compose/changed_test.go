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
