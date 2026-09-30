# The wire probe needs a network that is WRONG about its wire: lan-range,
# 192.168.86.0/24 declared on lanbridge, whose DHCP server says
# 192.168.4.0/24. fjord refuses to create such a network now (#45), so it is
# written as a conflist by hand, the way one left from before would be, and
# removed through fjordd afterwards.
before() {
  on 'cat > /usr/local/etc/cni/net.d/lan-range.conflist' <<'EOF'
{
  "cniVersion": "0.4.0",
  "name": "lan-range",
  "plugins": [
    {
      "type": "epair",
      "master": "lanbridge",
      "ipam": {
        "type": "host-local",
        "routes": [{"dst": "0.0.0.0/0"}],
        "ranges": [[{"subnet": "192.168.86.0/24", "gateway": "192.168.86.1", "rangeStart": "192.168.86.200", "rangeEnd": "192.168.86.210"}]]
      },
      "capabilities": {"ips": true, "mac": true}
    }
  ]
}
EOF
}

after() {
  del_net lan-range podman
  if on 'test -e /usr/local/etc/cni/net.d/lan-range.conflist'; then
    echo "FAIL lan-range: still on the host after Delete"; on 'rm -f /usr/local/etc/cni/net.d/lan-range.conflist'
  else echo "PASS lan-range: removed through fjordd"; fi
}
