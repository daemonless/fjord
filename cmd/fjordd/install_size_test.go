package main

import (
	"context"
	"errors"
	"slices"
	"testing"
)

const sizeManifest = `services:
  app:
    image: ghcr.io/daemonless/app:${APP_TAG:-latest}
  ml:
    image: ghcr.io/daemonless/app-ml:latest
x-fjord:
  version: "0.1"
  variables:
    - name: APP_TAG
      type: image_tag
      image: ghcr.io/daemonless/app
      default: latest
      optional: true
    - name: APP_DB_PASSWORD
      type: secret
  choices:
    - id: database
      kind: database
      label: Database
      default: sqlite
      options:
        - id: sqlite
          label: SQLite
        - id: external
          label: Your own
          ask:
            - {name: APP_DB_HOST, label: Host}
        - id: postgres
          label: PostgreSQL
          secrets: [APP_DB_PASSWORD]
          services: |
              postgres:
                image: ghcr.io/daemonless/postgres:17
    - id: machine_learning
      kind: part
      label: Machine learning
      default: "on"
      options:
        - id: "on"
          label: With
        - id: "off"
          label: Without
          drop: [ml]
`

// The images follow the choices and the tag; a required secret left empty
// (a form still being filled in) is no error.
func TestInstallImagesFollowChoicesAndTag(t *testing.T) {
	for _, c := range []struct {
		name string
		req  installRequest
		want []string
	}{
		{"defaults", installRequest{}, []string{"ghcr.io/daemonless/app:latest", "ghcr.io/daemonless/app-ml:latest"}},
		{"postgres, no ml", installRequest{Choices: map[string]string{"database": "postgres", "machine_learning": "off"}},
			[]string{"ghcr.io/daemonless/app:latest", "ghcr.io/daemonless/postgres:17"}},
		{"your own database, its host not typed yet", installRequest{Choices: map[string]string{"database": "external"}},
			[]string{"ghcr.io/daemonless/app:latest", "ghcr.io/daemonless/app-ml:latest"}},
		{"an image_tag value", installRequest{Values: map[string]string{"APP_TAG": "pkg"}},
			[]string{"ghcr.io/daemonless/app:pkg", "ghcr.io/daemonless/app-ml:latest"}},
	} {
		c.req.Manifest = sizeManifest
		got, err := installImages(c.req, "")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		slices.Sort(got)
		slices.Sort(c.want)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// A size the registry would not give is unknown, and kept like a known one
// for the ten minutes: no registry round trip per keystroke.
func TestCachedImageSize(t *testing.T) {
	calls := 0
	old := registryImageSize
	registryImageSize = func(_ context.Context, image string) (int64, int, error) {
		calls++
		if image == "private/thing:1" {
			return 0, 0, errors.New("unauthorized")
		}
		return 1234, 3, nil
	}
	defer func() { registryImageSize = old }()
	for i := 0; i < 2; i++ {
		if n, ok := cachedImageSize(context.Background(), "ghcr.io/x/a:1"); n != 1234 || !ok {
			t.Fatalf("got %d %v", n, ok)
		}
		if _, ok := cachedImageSize(context.Background(), "private/thing:1"); ok {
			t.Fatal("an unauthorized image read as known")
		}
	}
	if calls != 2 {
		t.Fatalf("%d registry calls, want 2", calls)
	}
}
