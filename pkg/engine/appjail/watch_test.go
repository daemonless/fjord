package appjail

import (
	"context"
	"strings"
	"testing"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
)

// fakeJail answers the watch's three questions from a script.
type fakeJail struct {
	jids   []string // jid per ask; the last repeats
	procs  int
	health string
	asks   int
}

func (f *fakeJail) install(t *testing.T) {
	oj, op, oh := jailJID, jailProcs, jailHealth
	jailJID = func(context.Context, string) string {
		i := min(f.asks, len(f.jids)-1)
		f.asks++
		return f.jids[i]
	}
	jailProcs = func(context.Context, string) int { return f.procs }
	jailHealth = func(context.Context, string, composepkg.Service) (string, string) { return f.health, "" }
	t.Cleanup(func() { jailJID, jailProcs, jailHealth = oj, op, oh })
}

func watch(t *testing.T, f *fakeJail) (string, error) {
	f.install(t)
	var out strings.Builder
	err := watchJails(context.Background(), &out, []svcJail{{svc: composepkg.Service{Name: "web"}, jail: "app_web"}}, 2*time.Second, "the update")
	return out.String(), err
}

func TestWatchJails(t *testing.T) {
	for _, c := range []struct {
		name string
		f    fakeJail
		says string
		fail bool
	}{
		{"stays up", fakeJail{jids: []string{"7"}, procs: 9, health: "running"}, "web has stayed up", false},
		{"goes down", fakeJail{jids: []string{"7", ""}, procs: 9}, "jail went down", true},
		{"started again", fakeJail{jids: []string{"7", "8"}, procs: 9}, "started again", true},
		// What appjail does when the app dies: the jail stays, empty.
		{"up with nothing inside", fakeJail{jids: []string{"7"}, procs: 0}, "nothing running inside", true},
		{"crash-looping inside", fakeJail{jids: []string{"7"}, procs: 9, health: "crashed"}, "keeps crashing", true},
	} {
		out, err := watch(t, &c.f)
		if (err != nil) != c.fail || !strings.Contains(out, c.says) {
			t.Errorf("%s: err %v\n%s", c.name, err, out)
		}
		if !strings.Contains(out, "[fjord] checking web stays up for 2s") {
			t.Errorf("%s: no start line\n%s", c.name, out)
		}
	}
}

// A jail that never came up is the update's to report, not the watch's.
func TestWatchJailsNothingToWatch(t *testing.T) {
	out, err := watch(t, &fakeJail{jids: []string{""}})
	if err != nil || out != "" {
		t.Fatalf("err %v, out %q", err, out)
	}
}
