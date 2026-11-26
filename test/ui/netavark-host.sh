# Only where podman's network backend is netavark and the port's
# netavark-migrate speaks interface 2. The host side undone (netavark's pf
# anchors, net.pf.filter_local, pf at boot) and fjordd restarted, so it
# finds a host to set up; everything put back as it was afterwards.
NH=/tmp/fjord-ui-nvhost
before() {
  [ "$(on "podman info --format '{{.Host.NetworkBackend}}'")" = netavark ] || { echo "SKIP netavark-host: podman's network backend here is not netavark"; return 1; }
  on "/usr/local/sbin/netavark-migrate version 2>/dev/null" | grep -q '^netavark-migrate [2-9] .*host.*json' ||
    { echo "SKIP netavark-host: no netavark-migrate with interface 2 here"; return 1; }
  on "mkdir -p $NH && cp /etc/pf.conf /etc/sysctl.conf /boot/loader.conf $NH/ && cp /var/db/fjord/host-notices.json $NH/ 2>/dev/null; sysctl -n net.pf.filter_local > $NH/filter_local"
  on "service fjordd stop >/dev/null
      sed -i '' '/netavark/d' /etc/pf.conf && pfctl -f /etc/pf.conf 2>/dev/null
      sed -i '' '/^net.pf.filter_local/d' /etc/sysctl.conf; sysctl net.pf.filter_local=0 >/dev/null
      sed -i '' '/^pf_load/d' /boot/loader.conf
      rm -f /var/db/fjord/host-notices.json
      service fjordd start >/dev/null; true"
}
after() {
  if on "grep -q 'netavark-migrate set the host up' /var/log/fjordd.log"; then
    echo "PASS fjordd had netavark-migrate set the host up: $(on "grep 'netavark-migrate set the host up' /var/log/fjordd.log | tail -1 | cut -c21-160")"
  else echo "FAIL no 'netavark-migrate set the host up' in fjordd.log: $(on "grep 'netavark' /var/log/fjordd.log | tail -3 | cut -c21-200 | tr '\n' '|'")"; fi
  n=$(on "grep -c '^[a-z-]*anchor \"netavark/\*\"\|^anchor \"netavark/\*\"' /etc/pf.conf")
  [ "$n" = 3 ] && echo "PASS pf.conf has netavark's three anchors" || echo "FAIL pf.conf has $n of netavark's anchors"
  [ "$(on 'sysctl -n net.pf.filter_local')" = 1 ] && on "grep -q '^net.pf.filter_local=1' /etc/sysctl.conf" &&
    echo "PASS net.pf.filter_local is 1, and kept in sysctl.conf" || echo "FAIL net.pf.filter_local not set"
  on "grep -q '^pf_load=\"YES\"' /boot/loader.conf" && echo "PASS pf loads at boot" || echo "FAIL no pf_load in loader.conf"
  on "service fjordd stop >/dev/null
      cp $NH/pf.conf /etc/pf.conf; cp $NH/sysctl.conf /etc/sysctl.conf; cp $NH/loader.conf /boot/loader.conf
      [ -f $NH/host-notices.json ] && cp $NH/host-notices.json /var/db/fjord/host-notices.json || rm -f /var/db/fjord/host-notices.json
      pfctl -f /etc/pf.conf 2>/dev/null; sysctl net.pf.filter_local=\$(cat $NH/filter_local) >/dev/null
      rm -rf $NH; service fjordd start >/dev/null; true"
}
