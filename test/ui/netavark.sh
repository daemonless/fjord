# Only where podman's network backend is netavark. Leaves two networks the
# way the CNI backend kept them: a LAN one (a conflist for the epair plugin)
# for the page to list, explain and delete, and a private one for it to make
# again under netavark.
CONFLIST=/usr/local/etc/cni/net.d/t-legacy.conflist
OLDPRIV=/usr/local/etc/cni/net.d/t-oldpriv.conflist
OLDLAN=/usr/local/etc/cni/net.d/t-oldlan.conflist
before() {
  [ "$(on "podman info --format '{{.Host.NetworkBackend}}'")" = netavark ] || { echo "SKIP netavark: podman's network backend here is not netavark"; return 1; }
  on "mkdir -p /usr/local/etc/cni/net.d"
  on "cat > $CONFLIST" <<'EOF'
{
  "cniVersion": "0.4.0",
  "name": "t-legacy",
  "x-fjord": { "for": "podman", "subnet": "192.168.4.0/24", "gateway": "192.168.4.1" },
  "plugins": [ { "type": "epair", "master": "tnobridge", "ipam": { "type": "dhcp" } } ]
}
EOF
  # A LAN network with a pool, on a bridge of the host's with nothing on it.
  on "ifconfig tnvbr >/dev/null 2>&1 || ifconfig bridge create name tnvbr up >/dev/null"
  on "cat > $OLDLAN" <<'EOF3'
{"cniVersion":"0.4.0","name":"t-oldlan","plugins":[{"type":"epair","master":"tnvbr","ipam":{"type":"host-local","routes":[{"dst":"0.0.0.0/0"}],"ranges":[[{"subnet":"10.136.0.0/24","gateway":"10.136.0.1","rangeStart":"10.136.0.200","rangeEnd":"10.136.0.220"}]]},"capabilities":{"ips":true,"mac":true}}]}
EOF3
  # And a private network the way podman's CNI backend kept one.
  on "cat > $OLDPRIV" <<'EOF2'
{"cniVersion":"0.4.0","name":"t-oldpriv","plugins":[{"type":"bridge","bridge":"cni-podman9","isGateway":true,"ipMasq":true,"ipam":{"type":"host-local","routes":[{"dst":"0.0.0.0/0"}],"ranges":[[{"subnet":"10.135.0.0/24","gateway":"10.135.0.1"}]]}},{"type":"portmap","capabilities":{"portMappings":true}}]}
EOF2
}

after() {
  if on "podman network inspect t-oldpriv --format '{{range .Subnets}}{{.Subnet}}{{end}}' 2>/dev/null | grep -qx 10.135.0.0/24"; then
    echo "PASS t-oldpriv: podman has it again, on 10.135.0.0/24"
  else echo "FAIL t-oldpriv: podman does not have it on 10.135.0.0/24"; fi
  if on "test -e $OLDPRIV"; then
    echo "FAIL t-oldpriv: its conflist is still on the host"; on "rm -f $OLDPRIV"
  else echo "PASS t-oldpriv: its conflist is gone from the host"; fi
  on "podman network rm -f t-oldpriv >/dev/null 2>&1; true"
  if on "podman network inspect t-oldlan --format '{{.NetworkInterface}} {{range .Subnets}}{{.LeaseRange.StartIP}}-{{.LeaseRange.EndIP}}{{end}}' 2>/dev/null | grep -qx 'tnvbr 10.136.0.200-10.136.0.220'"; then
    echo "PASS t-oldlan: podman has it on tnvbr with its range"
  else echo "FAIL t-oldlan: podman does not have it on tnvbr with its range"; fi
  on "podman network rm -f t-oldlan >/dev/null 2>&1; rm -f $OLDLAN; ifconfig tnvbr destroy 2>/dev/null; true"
  if on "test -e $CONFLIST"; then
    echo "FAIL t-legacy: its conflist is still on the host after Delete"; on "rm -f $CONFLIST"
  else echo "PASS t-legacy: its conflist is gone from the host"; fi
  if on "test -e /usr/local/etc/containers/networks/t-nvui.json || podman network exists t-nvui"; then
    echo "FAIL t-nvui: still on the host after Delete"; on "podman network rm -f t-nvui >/dev/null 2>&1"
  else echo "PASS t-nvui: podman no longer has it"; fi
}
