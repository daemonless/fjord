package hostnet

// On Linux podman keeps its networks as netavark JSON, not CNI conflists.
// Reading FreeBSD's CNI directory there found nothing, so every network was
// "attached but not defined on this host" and a service's interface rows
// came up empty -- saving them wrote network_mode: none.
func init() {
	cniDir, netavarkDir = "", "/etc/containers/networks"
	cur = layout{primary: source{netavarkDir, ".json", parseNetavark}}
}
