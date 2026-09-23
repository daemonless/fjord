package compose

import (
	"strings"
	"testing"
)

// Rollback pins ONE service; the rest of a multi-image stack must come back
// byte-for-byte what the operator wrote, ${VAR}s and comments included.
func TestSetServiceImage(t *testing.T) {
	in := `services:
  immich-server:
    # the app
    image: ghcr.io/x/immich-server:${IMMICH_TAG:-latest}
  database:
    image: ghcr.io/x/immich-postgres:latest
`
	out, err := SetServiceImage(in, "database", "ghcr.io/x/immich-postgres:latest@sha256:abc")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"image: ghcr.io/x/immich-postgres:latest@sha256:abc",
		"image: ghcr.io/x/immich-server:${IMMICH_TAG:-latest}",
		"# the app",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if _, err := SetServiceImage(in, "nope", "x"); err == nil {
		t.Error("unknown service accepted")
	}
}
