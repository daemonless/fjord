# Jail name resolution set up through the Setup page's button, from a host
# with dnsmasq stopped. Leaves dnsmasq running on fjord's config, which is
# what AppJail multi-service stacks need anyway.
before() {
  on 'service dnsmasq onestop >/dev/null 2>&1; sysrc -x dnsmasq_enable dnsmasq_conf >/dev/null 2>&1; true'
}

main() {
  run_js appjail-dns -e STEP=broken
  run_js appjail-dns -e STEP=fixed
}

after() {
  if on 'service dnsmasq status >/dev/null 2>&1 && [ "$(sysrc -n dnsmasq_conf)" = /usr/local/etc/fjord/dnsmasq-appjail.conf ]'; then
    echo "PASS dnsmasq runs with fjord's config on $HOST"
  else echo "FAIL dnsmasq is not running with fjord's config on $HOST"; fi
  # Not an open resolver: nothing answers :53 on the host's LAN address.
  if on "sockstat -4l -p 53 | grep -q '$IP:53'"; then
    echo "FAIL dnsmasq answers on the LAN ($IP:53)"
  else echo "PASS nothing answers DNS on the LAN ($IP:53)"; fi
}
