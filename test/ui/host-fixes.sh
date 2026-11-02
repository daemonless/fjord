# pf's podman anchors and ifconfig_DEFAULT, set up from the Setup page's
# button. Starts from what a fresh VM image has: no /etc/pf.conf, pf off,
# ifconfig_DEFAULT="DHCP" and no line for the NIC. Leaves pf enabled with
# the anchors, which the other tests need anyway.
NIC=$(on 'route -n get default | awk "/interface:/ {print \$2}"')
before() {
  on "service pf onestop >/dev/null 2>&1; pfctl -Fa -q 2>/dev/null; sysrc -x pf_enable >/dev/null 2>&1; \
    [ -f /etc/pf.conf ] && mv /etc/pf.conf /etc/pf.conf.before-host-fixes; \
    sysrc ifconfig_DEFAULT=DHCP >/dev/null; sysrc -x ifconfig_$NIC >/dev/null 2>&1; true"
}

main() {
  run_js host-fixes -e STEP=broken
  run_js host-fixes -e STEP=fixed
  # pf up, a NAT line gone: a reload, not a restart (a restart flushes every
  # connection's state, this ssh session's too)
  on "sed -i '' '/^nat on $NIC inet from <cni-nat>/d' /etc/pf.conf; pfctl -f /etc/pf.conf"
  run_js host-fixes -e STEP=partial
  run_js host-fixes -e STEP=fixed
}

after() {
  if on "grep -q '^rdr-anchor \"cni-rdr/\*\"' /etc/pf.conf && grep -q '^nat on $NIC inet from <cni-nat>' /etc/pf.conf"; then
    echo "PASS /etc/pf.conf has the anchors and NAT on $NIC"
  else echo "FAIL /etc/pf.conf lacks the anchors or NAT on $NIC"; fi
  if on "grep -c '^rdr-anchor \"cni-rdr/\*\"' /etc/pf.conf | grep -qx 1"; then
    echo "PASS the anchor line is in /etc/pf.conf once"
  else echo "FAIL the anchor line is in /etc/pf.conf more than once"; fi
  if on "pfctl -s nat | grep -q cni-rdr && pfctl -s nat | grep -q 'nat on $NIC' && [ \"\$(sysrc -n pf_enable)\" = YES ]"; then
    echo "PASS pf runs the rules and is enabled at boot"
  else echo "FAIL pf is not running the rules, or not enabled at boot"; fi
  if on "[ \"\$(sysrc -n ifconfig_$NIC)\" = DHCP ] && ! sysrc -n ifconfig_DEFAULT >/dev/null 2>&1"; then
    echo "PASS rc.conf names $NIC and ifconfig_DEFAULT is gone"
  else echo "FAIL rc.conf: $(on "sysrc ifconfig_$NIC ifconfig_DEFAULT 2>&1 | tr '\n' ' '")"; fi
  on 'rm -f /etc/pf.conf.before-host-fixes'
}
