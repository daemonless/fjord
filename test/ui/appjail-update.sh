# One service of an AppJail stack updated from the panel: the jail of the
# service that was behind is replaced and runs the new image, the other
# jail is never stopped (same jid).
#
# The stack is a director project written here, not a catalog app: `sync`
# names its image (`from:`), as every sidecar of a catalog bundle does, and
# is built from a stale copy of the tag so the panel has a real update.
AJU_REF=ghcr.io/daemonless/syncthing
before() {
  on "buildah pull -q $AJU_REF:2.1.3 >/dev/null && buildah tag $AJU_REF:2.1.3 $AJU_REF:latest" ||
    { echo "could not stage $AJU_REF:2.1.3 as :latest"; return 1; }
}
aju_jid() { on "jls -j $1 jid 2>/dev/null"; }
aju_image() { on "buildah inspect --type container --format '{{.FromImageID}}' appjail-$1 2>/dev/null"; }
main() {
  body=$(python3 -c '
import json
director = """options:
  - virtualnet: ":<random> default"
  - nat:
services:
  web:
    name: t_aju_web
    options:
      - container: "args:--pull"
  sync:
    name: t_aju_sync
    priority: 1
    options:
      - from: ghcr.io/daemonless/syncthing:latest
"""
makejail = """ARG tag=2.0.5

OPTION container=boot
OPTION overwrite=force
OPTION from=ghcr.io/daemonless/openspeedtest:${tag}
"""
print(json.dumps({"engine": "appjail", "director": director, "makejail": makejail, "compose": "", "env": ""}))')
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 -X POST -H "Origin: $FJORD" -H 'Content-Type: application/json' -d "$body" "$FJORD/api/stacks/t-aju/save")
  [ "$code" = 200 ] || { say "FAIL t-aju: creating the stack answered $code"; return; }
  curl -s -o /dev/null --max-time 600 -X POST -H "Origin: $FJORD" "$FJORD/api/stacks/t-aju/up"
  AJU_WEB=$(aju_jid t_aju_web); AJU_SYNC=$(aju_jid t_aju_sync); AJU_OLD=$(aju_image t_aju_sync)
  if [ -n "$AJU_WEB" ] && [ -n "$AJU_SYNC" ]; then say "PASS t-aju: both jails up (web jid $AJU_WEB, sync jid $AJU_SYNC)"
  else say "FAIL t-aju: jails not up after Start (web '$AJU_WEB', sync '$AJU_SYNC')"; return; fi
  run_js appjail-update
}
after() {
  for _ in $(seq 1 30); do curl -s --max-time 10 "$FJORD/api/stacks/t-aju" | grep -q '"busy"' || break; sleep 5; done
  web=$(aju_jid t_aju_web); sync=$(aju_jid t_aju_sync); new=$(aju_image t_aju_sync)
  if [ -n "$web" ] && [ "$web" = "${AJU_WEB:-}" ]; then echo "PASS t-aju: web was left running (jid $web before and after)"
  else echo "FAIL t-aju: web jid ${AJU_WEB:-?} -> ${web:-<no jail>}, it was not updated and must not restart"; fi
  if [ -n "$sync" ] && [ "$sync" != "${AJU_SYNC:-}" ]; then echo "PASS t-aju: sync's jail was replaced (jid ${AJU_SYNC:-?} -> $sync)"
  else echo "FAIL t-aju: sync jid ${AJU_SYNC:-?} -> ${sync:-<no jail>}, want a new jail"; fi
  if [ -n "$new" ] && [ "$new" != "${AJU_OLD:-}" ]; then echo "PASS t-aju: sync runs a newer image than it was built from"
  else echo "FAIL t-aju: sync still runs image ${new:-<none>}, the one it had"; fi
  del_stack t-aju
  left=$(on 'jls name | grep "^t_aju_"' | tr '\n' ' ')
  [ -z "$left" ] || echo "FAIL t-aju: jails left behind: $left"
}
