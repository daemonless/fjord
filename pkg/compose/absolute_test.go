package compose

import (
	"strings"
	"testing"
)

// A compose project imported from its own folder: paths relative to that
// folder must name the same places once fjord keeps the file elsewhere.
func TestAbsolutePaths(t *testing.T) {
	in := `services:
  plex:
    image: plex
    env_file: plex.env
    volumes:
      - ./config:/config
      - ../media:/media:ro
      - /srv/tv:/tv
      - cache:/cache
      - type: bind
        source: ./transcode
        target: /transcode
  helper:
    image: h
    env_file:
      - ./a.env
      - path: b.env
volumes:
  cache: {}
`
	out, err := AbsolutePaths(in, "/containers/plex")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`/containers/plex/config:/config`, `/containers/media:/media:ro`, `/srv/tv:/tv`, `cache:/cache`,
		`source: /containers/plex/transcode`, `env_file: /containers/plex/plex.env`,
		`- /containers/plex/a.env`, `path: /containers/plex/b.env`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
