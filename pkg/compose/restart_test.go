package compose

import (
	"strings"
	"testing"
)

func TestParseRestart(t *testing.T) {
	cases := map[string]Restart{
		"unless-stopped": {Policy: RestartUnlessStopped},
		"always":         {Policy: RestartAlways},
		"no":             {Policy: RestartNo},
		"on-failure":     {Policy: RestartOnFailure},
		"on-failure:5":   {Policy: RestartOnFailure, Retries: 5},
	}
	for in, want := range cases {
		got, ok := ParseRestart(in)
		if !ok || got != want {
			t.Errorf("ParseRestart(%q) = %+v, %v; want %+v", in, got, ok, want)
		}
		if got.String() != in {
			t.Errorf("%+v.String() = %q, want %q", got, got.String(), in)
		}
	}
	for _, bad := range []string{"", "sometimes", "always:3", "on-failure:x", "on-failure:-1"} {
		if _, ok := ParseRestart(bad); ok {
			t.Errorf("ParseRestart(%q) accepted", bad)
		}
	}
}

const gitea = `services:
  gitea:
    image: ghcr.io/daemonless/gitea:latest
    restart: unless-stopped
  postgres:
    image: ghcr.io/daemonless/postgres:17
    restart: always
`

// A database choice adds its service with `always` beside the app's
// `unless-stopped`: the stack is mixed until the user picks one.
func TestRestartOfMixedAfterAChoice(t *testing.T) {
	got := RestartOf(gitea)
	if !got.Mixed || got.Services["gitea"] != "unless-stopped" || got.Services["postgres"] != "always" {
		t.Fatalf("RestartOf = %+v", got)
	}
	set, err := SetRestart(gitea, Restart{Policy: RestartOnFailure, Retries: 5})
	if err != nil {
		t.Fatal(err)
	}
	if got := RestartOf(set); got.Mixed || got.Policy != RestartOnFailure || got.Retries != 5 {
		t.Fatalf("after SetRestart: %+v\n%s", got, set)
	}
}

// A one-shot ("no" beside services that restart) keeps its "no": under
// unless-stopped a migration would run again on every exit.
func TestSetRestartLeavesAOneShot(t *testing.T) {
	in := `services:
  app:
    image: a
    restart: unless-stopped
  migrate:
    image: a
    restart: "no"
`
	if got := RestartOf(in); got.Mixed || got.Policy != RestartUnlessStopped {
		t.Fatalf("RestartOf = %+v", got)
	}
	out, err := SetRestart(in, Restart{Policy: RestartAlways})
	if err != nil {
		t.Fatal(err)
	}
	r := ServiceRestarts(out)
	if r["app"] != "always" || r["migrate"] != "no" {
		t.Fatalf("restarts = %v\n%s", r, out)
	}
}

// Every service "no" is the stack's policy, not a one-shot: changing it
// changes them all, and "no" is written quoted.
func TestSetRestartFromNever(t *testing.T) {
	in := "services:\n  a:\n    image: a\n    restart: no\n  b:\n    image: b\n    restart: \"no\"\n"
	if got := RestartOf(in); got.Mixed || got.Policy != RestartNo {
		t.Fatalf("RestartOf = %+v", got)
	}
	out, err := SetRestart(in, Restart{Policy: RestartUnlessStopped})
	if err != nil {
		t.Fatal(err)
	}
	if r := ServiceRestarts(out); r["a"] != "unless-stopped" || r["b"] != "unless-stopped" {
		t.Fatalf("restarts = %v", r)
	}
	back, _ := SetRestart(out, Restart{Policy: RestartNo})
	if !strings.Contains(back, `restart: "no"`) {
		t.Fatalf("no is not quoted:\n%s", back)
	}
}

// A service with no restart: is its own answer: alone it is "not set",
// beside one that restarts the stack is mixed; setting a policy writes both.
func TestRestartOfUnset(t *testing.T) {
	in := "services:\n  a:\n    image: a\n  b:\n    image: b\n    restart: always\n"
	if got := RestartOf(in); !got.Mixed {
		t.Fatalf("RestartOf = %+v", got)
	}
	if got := RestartOf("services:\n  a:\n    image: a\n"); got.Policy != "" || got.Mixed {
		t.Fatalf("RestartOf one unset = %+v", got)
	}
	out, _ := SetRestart(in, Restart{Policy: RestartAlways})
	if r := ServiceRestarts(out); r["a"] != "always" || r["b"] != "always" {
		t.Fatalf("restarts = %v", r)
	}
}

// A restart-only change is a change: Apply recreates the service.
func TestRestartChangeIsAChange(t *testing.T) {
	out, _ := SetRestart(gitea, Restart{Policy: RestartAlways})
	got := ChangedServices(gitea, nil, out, nil)
	if len(got) != 1 || got[0] != "gitea" {
		t.Fatalf("ChangedServices = %v", got)
	}
}

func TestSetRestartRefusesUnknown(t *testing.T) {
	if _, err := SetRestart(gitea, Restart{Policy: "sometimes"}); err == nil {
		t.Fatal("accepted an unknown policy")
	}
}
