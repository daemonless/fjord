# podman's DNS server moved off port 53 through the Setup page's button, in
# the order a real host meets it: two containers already resolve each other
# through aardvark-dns on port 53, THEN the dnsmasq for AppJail's jail names
# takes port 53 on every address. Needs the netavark backend and that dnsmasq
# (the appjail-dns test leaves it configured). Leaves the port moved, which
# the host needs for as long as that dnsmasq runs.
DROP=/usr/local/etc/containers/containers.conf.d/50-fjord-dns-port.conf
IMG=ghcr.io/daemonless/base:15
before() {
  [ "$(on "podman info --format '{{.Host.NetworkBackend}}'")" = netavark ] || { echo "SKIP dns-port: podman's network backend here is not netavark"; return 1; }
  [ "$(on 'sysrc -n dnsmasq_conf 2>/dev/null')" = /usr/local/etc/fjord/dnsmasq-appjail.conf ] || { echo "SKIP dns-port: no dnsmasq set up for AppJail's jail names here"; return 1; }
  on "rm -f $DROP; service dnsmasq onestop >/dev/null 2>&1; pkill -x aardvark-dns; service podman_service onerestart >/dev/null 2>&1; sleep 2; true"
  if on "grep -rqs '^[[:space:]]*dns_bind_port' /usr/local/etc/containers/containers.conf /usr/local/etc/containers/containers.conf.d/"; then
    on 'service dnsmasq onestart >/dev/null 2>&1'
    echo "SKIP dns-port: dns_bind_port is already set in this host's containers.conf"; return 1
  fi
  on "podman image exists $IMG || podman pull -q $IMG >/dev/null" || { echo "cannot pull $IMG"; return 1; }
  on "podman network create t-dnet >/dev/null && podman run -d --name t-dn1 --network t-dnet $IMG sleep 86400 >/dev/null && podman run -d --name t-dn2 --network t-dnet $IMG sleep 86400 >/dev/null && podman run -d --name t-dq1 $IMG sleep 86400 >/dev/null" ||
    { echo "cannot start the containers"; return 1; }
  on 'podman exec t-dn1 getent hosts t-dn2 >/dev/null' || { echo "the containers do not resolve each other before the test starts"; return 1; }
  DQ_STARTED=$(on "podman inspect t-dq1 --format '{{.State.StartedAt}}'")
  on 'service dnsmasq onestart >/dev/null 2>&1; sleep 2; true'
}

main() {
  run_js dns-port -e STEP=broken
  run_js dns-port -e STEP=fixed
}

after() {
  port=$(on "sed -n 's/^dns_bind_port = //p' $DROP 2>/dev/null")
  case $port in ''|53) echo "FAIL $DROP does not move the port (dns_bind_port='${port}')" ;; *) echo "PASS $DROP sets dns_bind_port = $port" ;; esac
  if on "sockstat -l -P udp | awk '\$2 ~ /^aardvark/ {print \$6}' | grep -q ':$port\$' && ! sockstat -l -P udp | awk '\$2 ~ /^aardvark/ {print \$6}' | grep -q ':53\$'"; then
    echo "PASS aardvark-dns listens on $port and no longer on 53"
  else echo "FAIL aardvark-dns: $(on "sockstat -l -P udp | awk '\$2 ~ /^aardvark/ {print \$6}' | tr '\n' ' '")"; fi
  if on 'podman exec t-dn1 getent hosts t-dn2 >/dev/null && podman exec t-dn2 getent hosts t-dn1 >/dev/null'; then
    echo "PASS the containers still find each other by name"
  else echo "FAIL the containers no longer find each other by name"; fi
  if on 'podman exec t-dn1 getent hosts freebsd.org >/dev/null'; then
    echo "PASS and names outside"
  else echo "FAIL names outside do not resolve from the container"; fi
  if [ "$DQ_STARTED" = "$(on "podman inspect t-dq1 --format '{{.State.StartedAt}}'")" ]; then
    echo "PASS a container that does not use name resolution was not restarted"
  else echo "FAIL t-dq1, on the default network, was restarted"; fi
  on 'podman rm -f -t0 t-dn1 t-dn2 t-dq1 >/dev/null 2>&1; podman network rm -f t-dnet >/dev/null 2>&1; true'
}
