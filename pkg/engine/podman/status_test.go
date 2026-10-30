package podman

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Where podman reports a container's address (Linux) it is taken as is; the
// CNI state fjord reads on FreeBSD does not exist there.
func TestContainerAddressesTakesPodmansReport(t *testing.T) {
	c := libpodContainer{ID: "abc", State: "running", Networks: []string{"app_default"},
		reported: map[string]string{"app_default": "10.89.0.2"}}
	if got := containerAddresses(c); got["app_default"] != "10.89.0.2" {
		t.Errorf("addresses %v", got)
	}
	if got := containerAddress(c); got != "10.89.0.2" {
		t.Errorf("address %q", got)
	}
}

// podman folds consecutive ports into one entry with a range; each port is
// its own, so a stack's 3001 is known as its own.
func TestPortRangeExpands(t *testing.T) {
	var c libpodContainer
	raw := `{"Names":["handbrake_handbrake_1"],"State":"running","Ports":[` +
		`{"host_ip":"","container_port":3000,"host_port":3000,"range":2,"protocol":"tcp"},` +
		`{"container_port":53,"host_port":5353,"protocol":"udp"}]}`
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	want := libpodPorts{{3000, 3000, "tcp"}, {3001, 3001, "tcp"}, {5353, 53, "udp"}}
	if !reflect.DeepEqual(c.Ports, want) {
		t.Errorf("ports %+v, want %+v", c.Ports, want)
	}
	if got := aggregateStatus([]libpodContainer{c}, nil).Containers[0].Ports; len(got) != 3 || got[1].HostPort != 3001 {
		t.Errorf("status ports %+v", got)
	}
}

// whoami on a LAN address publishes nothing; its EXPOSE is the only port.
func TestExposedPortsReachStatus(t *testing.T) {
	var c libpodContainer
	raw := `{"Names":["spike-lan_web_1"],"State":"running","ExposedPorts":{"8080":["tcp"],"80":["tcp"],"53":["udp"]}}`
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if got := aggregateStatus([]libpodContainer{c}, nil).Containers[0].Exposed; !reflect.DeepEqual(got, []int{80, 8080}) {
		t.Errorf("exposed %v, want [80 8080]", got)
	}
}
