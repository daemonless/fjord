package main

import (
	"strings"
	"testing"
)

// After a switch back, the networks fjord made again under CNI go to a CNI
// notice, and the netavark notice (which says podman uses netavark) goes.
func TestNoticeFollowsTheBackend(t *testing.T) {
	s := &server{fjordRoot: t.TempDir()}
	s.noteHost(netavarkNotice, netavarkNoticeTitle, "Added netavark's anchors to /etc/pf.conf and loaded them.")

	s.noteCarriedOver("lan86", "cni")
	s.dropHostNotice(netavarkNotice) // what a switch back does

	all := s.hostNotices()
	if len(all) != 1 || all[0].ID != cniNotice {
		t.Fatalf("notices after the switch back: %+v", all)
	}
	if len(all[0].Lines) != 1 || !strings.Contains(all[0].Lines[0], "lan86") || !strings.Contains(all[0].Lines[0], "CNI") {
		t.Errorf("the CNI notice says %q", all[0].Lines)
	}

	s.dropHostNotice("not-there") // nothing to drop: nothing changes
	if n := s.hostNotices(); len(n) != 1 {
		t.Errorf("dropping a missing notice changed them: %+v", n)
	}
}
