# Needs a fjord linked to a provider elsewhere (Settings, Names); the host
# must resolve the provider's ending (the name is fetched through it).
UI_ENV="-e APP=${APP:-apprise}"
before() {
  curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/names" | grep -q '"linked":true' || { echo "SKIP names-linked: $HOST is not linked to a provider"; return 1; }
}
after() {
  del_stack t-nm
  sleep 2
  curl -s --max-time 20 "$FJORD/api/names" -b "$FJORD_COOKIE" | grep -q '"t-nm"' && echo "FAIL the name outlived its stack" || echo "PASS the name is gone with its stack"
}
