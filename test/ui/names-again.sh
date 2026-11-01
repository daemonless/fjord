# Needs a names provider already installed and ready on the host (the names
# test brings its own and needs ports 80 and 443 for it). APP: an app with a
# page from the store this OS has.
case $HOST_OS in freebsd) UI_ENV="-e APP=openspeedtest" ;; *) UI_ENV="-e APP=apprise" ;; esac
before() {
  curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/names" | grep -q '"ready":true' || { echo "SKIP names-again: $HOST has no ready names provider"; return 1; }
}
after() {
  del_stack t-nm
}
