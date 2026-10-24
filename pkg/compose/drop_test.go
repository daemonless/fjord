package compose

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDropVolumesReferencing(t *testing.T) {
	in := `services:
  radarr:
    image: x
    volumes:
      - ${CONFIG_DATA}:/config
      - ${MOVIES_PATH}:/movies
      - ${DOWNLOADS_PATH}:/downloads
`
	out, err := DropVolumesReferencing(in, []string{"MOVIES_PATH", "DOWNLOADS_PATH"})
	if err != nil {
		t.Fatalf("DropVolumesReferencing: %v", err)
	}
	if strings.Contains(out, "movies") || strings.Contains(out, "downloads") {
		t.Fatalf("empty-optional mounts not dropped:\n%s", out)
	}
	if !strings.Contains(out, "${CONFIG_DATA}:/config") {
		t.Fatalf("config mount wrongly dropped:\n%s", out)
	}
	// Still valid YAML with exactly one volume left.
	var m map[string]any
	if err := yaml.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("result not valid YAML: %v", err)
	}
	vols := m["services"].(map[string]any)["radarr"].(map[string]any)["volumes"].([]any)
	if len(vols) != 1 {
		t.Fatalf("expected 1 volume left, got %d: %+v", len(vols), vols)
	}
}

func TestDropVolumesNoVars(t *testing.T) {
	in := "services:\n  a:\n    image: x\n"
	out, err := DropVolumesReferencing(in, nil)
	if err != nil || out != in {
		t.Fatalf("no-op expected, got err=%v out=%q", err, out)
	}
}

// An optional setting left empty reaches the app unset, not set to nothing;
// one with its own default, or inside a longer value, stays.
func TestDropEnvReferencing(t *testing.T) {
	in := `services:
  app:
    image: x
    environment:
      - DB_TYPE=${DB_TYPE}
      - KEEP=${KEEP}
      - URL=postgres://${DB_HOST}/app
      - TZ=${TZ:-UTC}
  side:
    image: y
    environment:
      DB_TYPE: ${DB_TYPE}
      OTHER: fixed
`
	out, err := DropEnvReferencing(in, []string{"DB_TYPE", "DB_HOST", "TZ"})
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"DB_TYPE=", "DB_TYPE:"} {
		if strings.Contains(out, gone) {
			t.Errorf("%s still there:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"KEEP=${KEEP}", "URL=postgres://${DB_HOST}/app", "TZ=${TZ:-UTC}", "OTHER: fixed"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%s dropped:\n%s", kept, out)
		}
	}
}
