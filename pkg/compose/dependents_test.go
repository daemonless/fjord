package compose

import (
	"reflect"
	"testing"
)

// immich: the server depends on the other three. Recreating the database
// alone fails in podman ("has dependent containers") and podman-compose
// still exits 0, so the server has to go with it.
func TestWithDependents(t *testing.T) {
	c := `services:
  immich-server:
    image: s
    depends_on: [redis, database, immich-machine-learning]
  immich-machine-learning:
    image: m
  redis:
    image: r
  database:
    image: d
  proxy:
    image: p
    depends_on:
      immich-server:
        condition: service_started
`
	for _, tc := range []struct {
		in, want []string
	}{
		{[]string{"database"}, []string{"immich-server", "database", "proxy"}}, // and what needs the server
		{[]string{"proxy"}, []string{"proxy"}},                                 // nothing needs the proxy
		{[]string{"redis", "database"}, []string{"immich-server", "redis", "database", "proxy"}},
	} {
		if got := WithDependents(c, tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("WithDependents(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
