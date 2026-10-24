package compose

import (
	"strings"
	"testing"
)

func TestPassEnv(t *testing.T) {
	list := `services:
  ssh:
    image: x
    environment:
      - PUID=${PUID}
`
	out, err := PassEnv(list, FirstService, []string{"USER_NAME", "PUID", "USER_NAME"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "USER_NAME=${USER_NAME}") != 1 || strings.Count(out, "PUID") != 2 {
		t.Errorf("list form:\n%s", out)
	}
	mapping := `services:
  app:
    image: x
    environment:
      A: b
`
	if out, err = PassEnv(mapping, "app", []string{"KEY"}); err != nil || !strings.Contains(out, "KEY: ${KEY}") {
		t.Errorf("mapping form: %v\n%s", err, out)
	}
	none := "services:\n  app:\n    image: x\n"
	if out, err = PassEnv(none, "app", []string{"KEY"}); err != nil || !strings.Contains(out, "KEY=${KEY}") {
		t.Errorf("no environment yet: %v\n%s", err, out)
	}
}
