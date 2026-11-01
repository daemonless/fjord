package main

import (
	"strings"
	"testing"
)

// The catalog's web port is only taken for the catalog app's own image:
// another image of the same app can answer on another port.
func TestRunsImage(t *testing.T) {
	lsPlex := "services:\n  plex:\n    image: lscr.io/linuxserver/plex:latest\n"
	cases := []struct {
		compose, image string
		want           bool
	}{
		{lsPlex, "docker.io/plexinc/pms-docker:plexpass", false},
		{lsPlex, "lscr.io/linuxserver/plex:1.43", true},
		{lsPlex, "lscr.io/linuxserver/plex@sha256:abc", true},
		{"services:\n  web:\n    image: nginx:1.27\n", "docker.io/library/nginx:latest", true},
		{"services:\n  web:\n    image: ghcr.io/daemonless/radarr:latest\n", "ghcr.io/daemonless/sonarr:latest", false},
	}
	for _, c := range cases {
		if got := runsImage(c.compose, c.image); got != c.want {
			t.Errorf("runsImage(%q, %q) = %v, want %v", c.compose, c.image, got, c.want)
		}
	}
}

// An adopted app's web port is written as the host port it is published
// on, which is what the Open link reads; unpublished, it stays as it is.
func TestPublishedFor(t *testing.T) {
	compose := "services:\n  web:\n    image: nginx\n    ports:\n      - \"18181:80\"\n      - \"127.0.0.1:9443:443/tcp\"\n      - \"${DNS}:53/udp\"\n  other:\n    image: x\n    ports:\n      - \"7000:3000\"\n"
	cases := []struct{ service, port, want string }{
		{"web", "80", "18181"},
		{"web", "443", "9443"},
		{"web", "53", "53"},     // udp only: not a web port
		{"web", "3000", "3000"}, // another service's port
		{"", "3000", "7000"},    // no service named: any service
		{"web", "8080", "8080"}, // not published
	}
	for _, c := range cases {
		if got := publishedFor(compose, "DNS=5353\n", c.service, c.port); got != c.want {
			t.Errorf("publishedFor(%s, %s) = %s, want %s", c.service, c.port, got, c.want)
		}
	}
}

// An imported project's web port comes from the service with a web page,
// whatever order podman lists its containers in: mealie imported from pibox
// got postgres's 5432, and Open went to the database.
func TestWebHintLooksAtTheWholeProject(t *testing.T) {
	exposed := func(image string) []string {
		switch {
		case strings.Contains(image, "postgres"):
			return []string{"5432/tcp"}
		case strings.Contains(image, "mealie"):
			return []string{"9000/tcp"}
		case strings.Contains(image, "mariadb"):
			return []string{"3306/tcp"}
		case strings.Contains(image, "nginx"):
			return []string{"80/tcp", "443/tcp"}
		case strings.Contains(image, "redis"):
			return []string{"6379/tcp"}
		}
		return nil
	}
	mealie := "services:\n  app:\n    image: ghcr.io/mealie-recipes/mealie:latest\n    ports:\n      - \"9000:9000\"\n  postgres:\n    image: docker.io/library/postgres:17\n  tunnel:\n    image: docker.io/cloudflare/cloudflared:latest\n"
	dbFirst := "services:\n  db:\n    image: mariadb:11\n  cache:\n    image: redis:7\n  web:\n    image: nginx:1.27\n    ports:\n      - \"18080:80\"\n"
	lonePostgres := "services:\n  db:\n    image: postgres:17\n    ports:\n      - \"5432:5432\"\n"
	cases := []struct {
		name, compose, manifestCompose, manifestPort string
		manifestHTTPS                                bool
		want                                         string
		https                                        bool
	}{
		{"mealie: postgres listed first", mealie, "", "", false, "9000", false},
		{"database and cache before the web service", dbFirst, "", "", false, "18080", false},
		{"catalog app's image, in the second service", dbFirst, "services:\n  web:\n    image: docker.io/library/nginx:latest\n", "443", true, "443", true},
		{"catalog app not in this project", mealie, "services:\n  web:\n    image: ghcr.io/daemonless/mealie:latest\n", "9000", false, "9000", false},
		{"a database alone has no web page", lonePostgres, "", "", false, "", false},
	}
	for _, c := range cases {
		got, https := webHint(c.compose, "", c.manifestCompose, c.manifestPort, c.manifestHTTPS, exposed)
		if got != c.want || https != c.https {
			t.Errorf("%s: webHint = %q, %v; want %q, %v", c.name, got, https, c.want, c.https)
		}
	}
}

func TestIsDatabaseImage(t *testing.T) {
	for image, want := range map[string]bool{
		"docker.io/library/postgres:17": true, "mariadb:11": true, "redis:7": true, "valkey/valkey:8": true,
		"ghcr.io/daemonless/mongo:latest": true, "docker.io/library/nginx:1.27": false,
		"rediscommander/redis-commander:latest": false, "ghcr.io/mealie-recipes/mealie:latest": false,
	} {
		if got := isDatabaseImage(image); got != want {
			t.Errorf("isDatabaseImage(%q) = %v, want %v", image, got, want)
		}
	}
}
