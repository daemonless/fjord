# Needs an app in a catalog that declares x-fjord.provides.names (the
# reverse proxy, from daemonless's catalog) and one installed app with a page.
# APP is installed twice under one name (t-nm), to see the name go and come
# back with the app: one from the store this OS has.
case $HOST_OS in freebsd) UI_ENV="-e APP=openspeedtest" ;; *) UI_ENV="-e APP=apprise" ;; esac
before() {
  curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/catalog/daemonless/manifests/reverse-proxy.yaml" | grep -q '^  provides:' ||
    { echo "SKIP names: no reverse-proxy that provides names in the daemonless catalog"; return 1; }
}
after() {
  del_stack t-nm t-rp
  sleep 2
  curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/names" | grep -q '"provider"' && echo "FAIL fjord still sees a provider" || echo "PASS provider gone with its stack"
}
