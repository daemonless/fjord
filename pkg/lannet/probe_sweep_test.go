package lannet

import (
	"reflect"
	"testing"
)

func TestStaleProbes(t *testing.T) {
	out := "vlan4bridge: flags=1008843<UP,BROADCAST,RUNNING> metric 0 mtu 1500\n" +
		"\tdescription: vlan4\n" +
		"\tmember: epair7a flags=143<LEARNING,DISCOVER,AUTOEDGE>\n" +
		"epair7a: flags=1008943<UP,BROADCAST,RUNNING> metric 0 mtu 1500\n" +
		"\tdescription: fjord-probe\n" +
		"\tether 02:aa:bb:cc:dd:0a\n" +
		"epair3a: flags=1008943<UP,BROADCAST,RUNNING> metric 0 mtu 1500\n" +
		"\tdescription: jail:sonarr\n"
	if got := staleProbes(out); !reflect.DeepEqual(got, []string{"epair7a"}) {
		t.Errorf("staleProbes = %v, want [epair7a]", got)
	}
}
