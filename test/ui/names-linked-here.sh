# Installs the proxy through the store, sets it up and links fjord to it by
# code through the proxy's own API, then checks its page. Needs a catalog
# whose reverse proxy does not declare names, and no link yet.
before() {
  curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/names" | grep -q '"linked":true' &&
    { echo "SKIP names-linked-here: fjord is linked already"; return 1; }
  return 0
}
main() {
  run_js names-linked-here -e STEP=install
  addr=$(curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/stacks/t-rpl" | python3 -c '
import json,sys
d=json.load(sys.stdin)
print(next((a for c in d.get("status",{}).get("containers",[]) for a in (c.get("addresses") or {}).values()), ""))' 2>/dev/null)
  [ -n "$addr" ] || { echo "FAIL the proxy has no address"; return; }
  link=$(on "c=\$(podman ps --format '{{.Names}}' | grep '^t-rpl'); t=\$(podman logs \$c 2>&1 | sed -n 's/.*setup token: //p' | tail -1)
    j=/tmp/t-rpl.jar; rm -f \$j; u=https://$addr:8443
    curl -sk -c \$j -b \$j -H \"Origin: \$u\" -H 'Content-Type: application/json' -d \"{\\\"token\\\":\\\"\$t\\\"}\" \$u/api/v1/setup/token >/dev/null
    curl -sk -c \$j -b \$j -H \"Origin: \$u\" -H 'Content-Type: application/json' -d '{\"password\":\"correct horse battery staple\"}' \$u/api/v1/setup/password >/dev/null
    curl -sk -c \$j -b \$j -X PUT -H \"Origin: \$u\" -H 'Content-Type: application/json' -d '{\"domain\":\"apps.lan\",\"trustedNetworks\":[]}' \$u/api/v1/settings >/dev/null
    curl -sk -c \$j -b \$j -X POST -H \"Origin: \$u\" \$u/api/v1/pair/code" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("link",""))' 2>/dev/null)
  [ -n "$link" ] && echo "PASS the proxy made a link code" || { echo "FAIL the proxy made no link code"; return; }
  r=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' -b "$FJORD_COOKIE" -H "Origin: $FJORD" -H 'Content-Type: application/json' -d "{\"link\":\"$link\"}" "$FJORD/api/names/link")
  case $r in 2*) echo "PASS fjord linked by code" ;; *) echo "FAIL fjord did not link: $r" ;; esac
  run_js names-linked-here -e STEP=check
}
after() {
  curl -s -o /dev/null --max-time 20 -b "$FJORD_COOKIE" -X DELETE -H "Origin: $FJORD" "$FJORD/api/names/link"
  del_stack t-rpl
}
