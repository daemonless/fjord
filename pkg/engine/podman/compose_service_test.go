package podman

import "testing"

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
