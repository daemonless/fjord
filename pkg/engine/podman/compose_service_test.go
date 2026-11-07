package podman

import (
	"slices"
	"testing"

	"github.com/daemonless/fjord/pkg/stack"
)

func TestComposeServiceEitherLabel(t *testing.T) {
	for _, c := range []struct {
		labels map[string]string
		want   string
	}{
		{map[string]string{"io.podman.compose.service": "app"}, "app"},
		{map[string]string{"com.docker.compose.service": "app"}, "app"}, // podman-compose 1.3.0
		{map[string]string{"io.podman.compose.service": "app", "com.docker.compose.service": "other"}, "app"},
		{map[string]string{}, ""},
	} {
		if got := composeService(c.labels); got != c.want {
			t.Errorf("%v: %q, want %q", c.labels, got, c.want)
		}
	}
}

func TestMissingServices(t *testing.T) {
	st := &stack.Stack{
		Compose: "services:\n  web:\n    image: x\n  db:\n    image: y\n",
	}
	present := []libpodContainer{
		{Labels: map[string]string{"io.podman.compose.service": "web"}},
	}
	if got := missingServices(st, nil, present); !slices.Equal(got, []string{"db"}) {
		t.Fatalf("missingServices = %v, want [db]", got)
	}
	if got := missingServices(st, []string{"web"}, present); len(got) != 0 {
		t.Fatalf("missingServices(only=[web]) = %v, want empty", got)
	}
}
