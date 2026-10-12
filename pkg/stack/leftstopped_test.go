package stack

import "testing"

// An update leaves a stack down only when it was stopped on purpose. A
// failed install or a crash still wants to run, and the update that fixes
// it has to bring it up (the UI outcome test: Change Version left it down).
func TestLeftStopped(t *testing.T) {
	for _, c := range []struct {
		state *State
		want  bool
	}{
		{nil, false},
		{&State{DesiredState: "running"}, false},
		{&State{DesiredState: "stopped"}, true},
		{&State{DesiredState: "deleting"}, false},
	} {
		if got := (&Stack{State: c.state}).LeftStopped(); got != c.want {
			t.Errorf("state %+v: LeftStopped = %v, want %v", c.state, got, c.want)
		}
	}
}
