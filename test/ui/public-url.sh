# A names provider (the reverse proxy, from the daemonless catalog, set up by
# public-url.js) and a catalog of its own serving Pocket ID's manifest with
# `needs: domain` on APP_URL and `behind_proxy: "true"` on TRUST_PROXY. Needs
# ports 80 and 443 free on the host for the proxy.
PU_CAT=${PU_CAT:-$HOME/src/scratch/pocketid-derive/out/local}
before() {
  [ -f "$PU_CAT/manifests/pocket-id.yaml" ] || { echo "SKIP public-url: no derived Pocket ID catalog at $PU_CAT"; return 1; }
  curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/catalog/daemonless/manifests/reverse-proxy.yaml" | grep -q '^  provides:' ||
    { echo "SKIP public-url: no reverse-proxy that provides names in the daemonless catalog"; return 1; }
  on "rm -rf /root/pu-cat && mkdir -p /root/pu-cat"
  tar cf - -C "$PU_CAT" . | on "tar xf - -C /root/pu-cat"
  on "cd /root/pu-cat && (pgrep -f 'http.server 8099' >/dev/null || nohup python3.12 -m http.server 8099 --bind 127.0.0.1 >/dev/null 2>&1 &)"
  sleep 1
  curl -s -o /dev/null --max-time 60 -b "$FJORD_COOKIE" -H "Origin: $FJORD" -H 'Content-Type: application/json' \
    -d '{"Name":"pu-test","URL":"http://127.0.0.1:8099"}' "$FJORD/api/catalogs"
}
after() {
  if on "grep -qx 'APP_URL=https://pocket-id.apps.lan' /var/db/fjord/stacks/t-pu/.env && grep -qx 'TRUST_PROXY=true' /var/db/fjord/stacks/t-pu/.env"; then
    echo "PASS installed: .env has APP_URL=https://pocket-id.apps.lan and TRUST_PROXY=true"
  else
    echo "FAIL installed: .env is $(on "grep -E '^(APP_URL|TRUST_PROXY)=' /var/db/fjord/stacks/t-pu/.env 2>&1" | tr '\n' ' ')"
  fi
  # A fresh install is watched for a while after it starts, and a delete
  # in that time is refused as busy: try again until it is gone.
  for _i in 1 2 3 4 5 6 7 8 9 10 11 12; do
    del_stack t-pu
    curl -s -o /dev/null -w '%{http_code}' --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/stacks/t-pu" | grep -q 404 && break
    sleep 10
  done
  del_stack t-rp
  curl -s -o /dev/null --max-time 20 -b "$FJORD_COOKIE" -X DELETE -H "Origin: $FJORD" "$FJORD/api/catalogs/pu-test"
  on "pkill -f 'http.server 8099'; rm -rf /root/pu-cat; true"
}
