package podman

import "testing"

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
