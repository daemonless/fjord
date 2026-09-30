# A version with no build for this host (amd64 against openspeedtest's
# arm64-only tag). The pulled arm64 image is removed afterwards.
after() {
  del_stack t-arch
  on 'podman rmi -f ghcr.io/daemonless/openspeedtest:latest-aarch64 >/dev/null 2>&1; true'
}
