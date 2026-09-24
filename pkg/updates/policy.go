package updates

import (
	"fmt"
	"time"
)

// Policy is how far a stack lets updates apply themselves.
type Policy string

const (
	Off      Policy = "off"    // nothing, and nothing shown (the default)
	Notify   Policy = "notify" // nothing applied; decisions shown and recorded
	Rebuilds Policy = "rebuilds"
	Patches  Policy = "patch"
	Minors   Policy = "minor"
	All      Policy = "all" // everything, including major and unknown
)

// ParsePolicy reads a stored or requested policy; "" is Off.
func ParsePolicy(s string) (Policy, bool) {
	switch p := Policy(s); p {
	case "":
		return Off, true
	case Off, Notify, Rebuilds, Patches, Minors, All:
		return p, true
	}
	return "", false
}

// rank orders the policies that apply something, and the classes they reach.
var rank = map[Policy]int{Rebuilds: 1, Patches: 2, Minors: 3, All: 4}
var needs = map[Class]int{Rebuild: 1, Patch: 2, Minor: 3, Major: 4, Unknown: 4}

// Candidate is one service's pending update, as the decision needs it.
type Candidate struct {
	Class     Class
	FirstSeen time.Time // when fjord first saw what the update would install
	Pinned    bool
	// Dependency: other services in the stack depend on this one -- in
	// practice its database, where a migration on startup is what bites.
	Dependency bool
}

// Verdict is what auto-update would do with a Candidate, and why.
type Verdict struct {
	Act    bool   `json:"act"`
	Reason string `json:"reason"`
}

// Soak is how long a version bump must have been seen before it is applied.
// Rebuilds don't soak: daemonless rebuilds nightly, so each would be replaced
// before it was a day old and never qualify; the health watch and automatic
// rollback are their guard instead.
const Soak = 24 * time.Hour

// Decide says whether policy p applies candidate c now.
func Decide(p Policy, c Candidate, now time.Time) Verdict {
	switch {
	case p == Off || p == "":
		return Verdict{false, "auto-update is off for this stack"}
	case p == Notify:
		return Verdict{false, "notify only"}
	case c.Pinned:
		return Verdict{false, "pinned to an exact image"}
	}
	if c.Dependency && c.Class != Rebuild {
		return Verdict{false, fmt.Sprintf("other services depend on this one, so it only takes rebuilds (this is %s)", article(c.Class))}
	}
	if rank[p] < needs[c.Class] {
		return Verdict{false, fmt.Sprintf("policy %s does not take %s", p, article(c.Class))}
	}
	if c.Class != Rebuild {
		if c.FirstSeen.IsZero() {
			return Verdict{false, "soaking: just seen"}
		}
		if left := c.FirstSeen.Add(Soak).Sub(now); left > 0 {
			return Verdict{false, fmt.Sprintf("soaking: %s left", roundUp(left))}
		}
	}
	return Verdict{true, fmt.Sprintf("would apply this %s", c.Class)}
}

func article(c Class) string {
	if c == Unknown {
		return "an update of unknown size"
	}
	return "a " + string(c)
}

// roundUp prints a remaining time in whole hours (minutes under an hour).
func roundUp(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes())+1)
	}
	return fmt.Sprintf("%dh", int(d.Hours())+1)
}
