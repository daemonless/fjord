package compose

import (
	"strings"
	"testing"
)

// An optional port left blank is dropped from ports:, nothing else moves.
func TestDropPortsReferencing(t *testing.T) {
	in := "services:\n  haproxy:\n    ports:\n      - \"${WEB_PORT}:80\"\n      - \"${PORT_443}:443\"\n      - \"${PORT_8404}:8404\"\n    volumes:\n      - \"${CONFIG_DATA}:/config\"\n"
	out, err := DropPortsReferencing(in, []string{"PORT_443", "PORT_8404"})
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"PORT_443", "PORT_8404"} {
		if strings.Contains(out, gone) {
			t.Errorf("%s still published:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"${WEB_PORT}:80", "${CONFIG_DATA}:/config"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%s lost:\n%s", kept, out)
		}
	}
	if same, _ := DropPortsReferencing(in, nil); same != in {
		t.Error("no vars must be a no-op")
	}
}

// A variable that only shares a PREFIX with an empty optional keeps its line,
// and a reference carrying its own default resolves to that default rather
// than being dropped.
func TestDropPortsReferencingBoundaries(t *testing.T) {
	in := "services:\n  app:\n    ports:\n" +
		"      - \"$PORT_HTTP:80\"\n" + // shares a prefix with PORT; must stay
		"      - \"${PORT:-8080}:8080\"\n" + // has a default; must stay
		"      - \"${PORT-8080}:8081\"\n" + // unset-only default; .env sets it empty
		"      - \"${PORT}:443\"\n" // plain reference; must go
	out, err := DropPortsReferencing(in, []string{"PORT"})
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"$PORT_HTTP:80", "${PORT:-8080}:8080"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%s dropped:\n%s", kept, out)
		}
	}
	for _, gone := range []string{"${PORT}:443", "${PORT-8080}:8081"} {
		if strings.Contains(out, gone) {
			t.Errorf("%s still published:\n%s", gone, out)
		}
	}
}

// The same boundary rules apply to volume sources.
func TestDropVolumesReferencingBoundaries(t *testing.T) {
	in := "services:\n  app:\n    volumes:\n" +
		"      - \"$MEDIA_TV:/tv\"\n" +
		"      - \"${MEDIA:-/srv/media}:/media\"\n" +
		"      - \"${MEDIA}:/data\"\n"
	out, err := DropVolumesReferencing(in, []string{"MEDIA"})
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"$MEDIA_TV:/tv", "${MEDIA:-/srv/media}:/media"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%s dropped:\n%s", kept, out)
		}
	}
	if strings.Contains(out, "${MEDIA}:/data") {
		t.Errorf("${MEDIA}:/data still mounted:\n%s", out)
	}
}
