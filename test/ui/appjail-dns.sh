# The appjail-dns check with dnsmasq stopped, then after the commands it
# shows. Leaves the host with dnsmasq running on appjail's config, which is
# what AppJail multi-service stacks need anyway.
before() {
  on 'service dnsmasq onestop >/dev/null 2>&1; sysrc -x dnsmasq_enable dnsmasq_conf >/dev/null 2>&1; true'
}

main() {
  run_js appjail-dns -e STEP=broken
  # The commands, as the page shows them, run as root on the host.
  on 'pkg install -y dnsmasq >/dev/null 2>&1; sysrc dnsmasq_enable=YES >/dev/null && sysrc dnsmasq_conf=/usr/local/share/appjail/files/dnsmasq.conf >/dev/null && service dnsmasq start >/dev/null 2>&1 && sysrc appjail_dns_enable=YES >/dev/null && service appjail-dns restart >/dev/null 2>&1' \
    && say "PASS the commands ran on $HOST" || say "FAIL the commands failed on $HOST"
  run_js appjail-dns -e STEP=fixed
}

after() {
  if on 'service dnsmasq status >/dev/null 2>&1 && pgrep -fl dnsmasq | grep -q appjail/files/dnsmasq.conf'; then
    echo "PASS dnsmasq answers with appjail's config on $HOST"
  else echo "FAIL dnsmasq is not running with appjail's config on $HOST"; fi
}
