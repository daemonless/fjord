# Needs an app in a catalog that declares x-fjord.provides.names (the
# reverse proxy, from daemonless's catalog) and one installed app with a page.
# APP is installed twice under one name (t-nm), to see the name go and come
# back with the app: one from the store this OS has.
case $HOST_OS in freebsd) UI_ENV="-e APP=openspeedtest" ;; *) UI_ENV="-e APP=apprise" ;; esac
before() {
  curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/catalog/daemonless/manifests/reverse-proxy.yaml" | grep -q '^  provides:' ||
    { echo "SKIP names: no reverse-proxy that provides names in the daemonless catalog"; return 1; }
  # The provider's page offers names for the apps already here: a host
  # with none (a fresh test VM) gets one, t-pre, installed through the API.
  if ! curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/stacks" | grep -q '"name"'; then
    _m=$(curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/catalog/daemonless/manifests/${UI_ENV##*APP=}.yaml")
    python3 -c 'import json,sys; print(json.dumps({"name":"t-pre","app_id":sys.argv[1],"manifest":sys.stdin.read(),"values":{},"engine":"podman"}))' "${UI_ENV##*APP=}" <<EOM | \
      curl -s -o /dev/null --max-time 600 -b "$FJORD_COOKIE" -H "Origin: $FJORD" -H 'Content-Type: application/json' -X POST -d @- "$FJORD/api/apps/install"
$_m
EOM
  fi
  # A LAN network for the unwatched install, so the name is proven on an
  # app's own address: DHCP on the host's bridge, when it has one (netlab:
  # lanbridge; a test VM: a bridge made with its NIC as member). Without
  # a bridge the unwatched install goes on the podman bridge instead.
  case $HOST_OS in freebsd)
    _br=$(on 'ifconfig -l' | tr ' ' '\n' | grep -m1 bridge)
    if [ -n "$_br" ]; then
      _gw=$(on 'route -n get default' | awk '/gateway:/ {print $2}')
      _net=$(on 'ifconfig $(route -n get default | awk "/interface:/ {print \$2}") inet' | awk '/inet / {split($2, a, "."); print a[1] "." a[2] "." a[3] ".0/24"; exit}')
      curl -s -o /dev/null --max-time 60 -b "$FJORD_COOKIE" -H "Origin: $FJORD" -H 'Content-Type: application/json' -X POST \
        -d "{\"name\":\"t-lan\",\"kind\":\"lan\",\"addressSource\":\"dhcp\",\"parent\":\"$_br\",\"subnet\":\"$_net\",\"gateway\":\"$_gw\"}" "$FJORD/api/networks?engine=podman"
    fi ;;
  esac
}
after() {
  del_stack t-nm t-rp t-pre
  del_net t-lan podman
  sleep 2
  curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/names" | grep -q '"provider"' && echo "FAIL fjord still sees a provider" || echo "PASS provider gone with its stack"
}
