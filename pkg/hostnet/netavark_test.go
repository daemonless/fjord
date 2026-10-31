package hostnet

import "testing"

// Definitions as podman 5.8 wrote them on Fedora 44 (netlab-linux): a
// compose project's default network, and a macvlan on a LAN with DHCP.
func TestParseNetavark(t *testing.T) {
	bridge := `{"name":"spike-web_default","driver":"bridge","network_interface":"podman1",
 "subnets":[{"subnet":"10.89.0.0/24","gateway":"10.89.0.1"}],"ipv6_enabled":false,
 "labels":{"io.podman.compose.project":"spike-web"},"ipam_options":{"driver":"host-local"}}`
	n, ok := parseNetavark("spike-web_default", []byte(bridge))
	if !ok || n.Type != "bridge" || n.Bridge != "podman1" || n.Subnet != "10.89.0.0/24" || n.Gateway != "10.89.0.1" || n.DHCP || n.Static {
		t.Errorf("bridge: %+v %v", n, ok)
	}
	lan := `{"name":"lan","driver":"macvlan","network_interface":"enp0s6f1",
 "subnets":[],"options":{"mtu":"1500"},
 "labels":{"fjord.for":"podman","fjord.subnet":"192.168.4.0/24","fjord.gateway":"192.168.4.1"},
 "ipam_options":{"driver":"dhcp"}}`
	n, ok = parseNetavark("lan", []byte(lan))
	if !ok || n.Type != "macvlan" || n.Bridge != "enp0s6f1" || !n.DHCP || n.MTU != 1500 || n.Subnet != "192.168.4.0/24" || n.For != "podman" {
		t.Errorf("macvlan dhcp: %+v %v", n, ok)
	}
	dual := `{"name":"d","driver":"bridge","subnets":[{"subnet":"fd00::/64","gateway":"fd00::1"},{"subnet":"10.1.0.0/24","gateway":"10.1.0.1"}],"ipam_options":{"driver":"host-local"}}`
	n, _ = parseNetavark("d", []byte(dual))
	if n.Subnet != "10.1.0.0/24" || n.Subnet6 != "fd00::/64" {
		t.Errorf("dual stack: %+v", n)
	}
	if _, ok := parseNetavark("x", []byte(`{"plugins":[{"type":"epair"}]}`)); ok {
		t.Error("a CNI conflist read as netavark")
	}
}

// A pool's range, which chooseAddresses walks: netavark's lease_range and a
// conflist's rangeStart/rangeEnd.
func TestPoolRange(t *testing.T) {
	n, ok := parseNetavark("lanpool", []byte(`{"name":"lanpool","driver":"macvlan","network_interface":"enp0s6f1","subnets":[{"subnet":"192.168.4.0/24","gateway":"192.168.4.1","lease_range":{"start_ip":"192.168.4.240","end_ip":"192.168.4.245"}}],"ipam_options":{"driver":"host-local"}}`))
	if !ok || n.RangeStart != "192.168.4.240" || n.RangeEnd != "192.168.4.245" || n.Bridge != "enp0s6f1" {
		t.Errorf("netavark: %+v", n)
	}
	n, ok = parse("lanpool", []byte(`{"plugins":[{"type":"epair","master":"lanbr","ipam":{"type":"host-local","ranges":[[{"subnet":"192.168.4.0/24","gateway":"192.168.4.1","rangeStart":"192.168.4.240","rangeEnd":"192.168.4.245"}]]}}]}`))
	if !ok || n.RangeStart != "192.168.4.240" || n.RangeEnd != "192.168.4.245" {
		t.Errorf("conflist: %+v", n)
	}
}
