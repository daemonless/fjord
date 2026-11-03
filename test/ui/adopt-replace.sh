# A container started by hand with podman, as someone would before fjord.
# The image is one the host has (openspeedtest, pulled by other tests) so
# the start is quick; published on a port nothing else uses.
before() {
  on 'podman rm -f t-byhand >/dev/null 2>&1; podman run -d --name t-byhand -p 3077:3000 ghcr.io/daemonless/openspeedtest:latest >/dev/null && sleep 3 && podman ps --format "{{.Names}} {{.Status}}" | grep -q "^t-byhand Up"' ||
    { echo "SKIP adopt-replace: could not start t-byhand by hand"; return 1; }
}
after() {
  del_stack t-byhand
  on 'podman rm -f t-byhand >/dev/null 2>&1; true'
}
