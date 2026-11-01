package manifest

import (
	"strings"
	"testing"
)

func TestParseNameEnv(t *testing.T) {
	m, err := Parse("services:\n  app:\n    image: x\nx-fjord:\n  info:\n    name_env:\n      HOST_ALLOWLIST: \"{names}\"\n")
	if err != nil || m.NameEnv["HOST_ALLOWLIST"] != "{names}" {
		t.Fatalf("got %v %v", m, err)
	}
	if _, err := Parse("services:\n  app:\n    image: x\nx-fjord:\n  info:\n    name_env:\n      \"not a var\": \"{name}\"\n"); err == nil || !strings.Contains(err.Error(), "not a variable name") {
		t.Fatalf("bad key: %v", err)
	}
}
