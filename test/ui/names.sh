# Needs an app in a catalog that declares x-fjord.provides.names (netlab's
# local-test catalog: reverse-proxy) and one installed app with a page.
before() {
  curl -s --max-time 20 "$FJORD/api/catalogs" | grep -q '"local-test"' || { echo "SKIP names: no local-test catalog"; return 1; }
}
after() {
  del_stack t-rp
  sleep 2
  curl -s --max-time 20 "$FJORD/api/names" | grep -q '"provider"' && echo "FAIL fjord still sees a provider" || echo "PASS provider gone with its stack"
}
