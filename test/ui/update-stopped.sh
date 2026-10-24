# Updating a stopped stack leaves it stopped, on both engines; Start then
# runs the new image. Each stack names syncthing:latest, staged as a stale
# copy (2.1.3) so there is a real update. podman and AppJail share the image
# store, so it is staged again before each.
#
# podman goes through the page (update-stopped.js). AppJail goes through the
# API: its update check reads the jails' images, Stop destroys the jails, so
# the page offers no update for a stopped AppJail stack.
UPS_REF=ghcr.io/daemonless/syncthing
ups_stage() {
  on "podman pull -q $UPS_REF:2.1.3 >/dev/null && podman tag $UPS_REF:2.1.3 $UPS_REF:latest" ||
    { say "FAIL update-stopped: could not stage $UPS_REF:2.1.3 as :latest"; return 1; }
  UPS_STALE=$(on "podman image inspect --format '{{.Id}}' $UPS_REF:latest")
}
ups_save() { # <stack> <json body>
  code=$(curl -s -b "$FJORD_COOKIE" -o /dev/null -w '%{http_code}' --max-time 30 -X POST -H "Origin: $FJORD" -H 'Content-Type: application/json' -d "$2" "$FJORD/api/stacks/$1/save")
  [ "$code" = 200 ] || { say "FAIL $1: creating the stack answered $code"; return 1; }
  curl -s -b "$FJORD_COOKIE" -o /dev/null --max-time 600 -X POST -H "Origin: $FJORD" "$FJORD/api/stacks/$1/up"
}
before() {
  on "podman image exists $UPS_REF:2.1.3 || podman pull -q $UPS_REF:2.1.3 >/dev/null" ||
    { echo "cannot pull $UPS_REF:2.1.3"; return 1; }
}
main() {
  # podman
  ups_stage || return
  body=$(python3 -c '
import json
compose = """services:
  sync:
    image: ghcr.io/daemonless/syncthing:latest
    restart: always
"""
print(json.dumps({"engine": "podman", "compose": compose, "env": ""}))')
  ups_save t-ups "$body" || return
  old=$(on "podman inspect --format '{{.Image}}' t-ups_sync_1 2>/dev/null")
  [ "$old" = "$UPS_STALE" ] && say "PASS t-ups: runs the stale image" || say "FAIL t-ups: runs '${old:-nothing}', want the staged $UPS_STALE"
  run_js update-stopped -e STACK=t-ups
  new=$(on "podman inspect --format '{{.Image}}' t-ups_sync_1 2>/dev/null")
  if [ -n "$new" ] && [ "$new" != "$UPS_STALE" ]; then say "PASS t-ups: Start ran the new image"
  else say "FAIL t-ups: after Start the container runs '${new:-nothing}', still the stale image"; fi
  del_stack t-ups

  # AppJail
  ups_stage || return
  body=$(python3 -c '
import json
director = """options:
  - virtualnet: ":<random> default"
  - nat:
services:
  web:
    name: t_ajs_web
    options:
      - container: "args:--pull"
  sync:
    name: t_ajs_sync
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
  ups_save t-ajs "$body" || return
  old=$(on "buildah inspect --type container --format '{{.FromImageID}}' appjail-t_ajs_sync 2>/dev/null")
  [ "$old" = "$UPS_STALE" ] && say "PASS t-ajs: sync runs the stale image" || say "FAIL t-ajs: sync runs '${old:-nothing}', want the staged $UPS_STALE"
  curl -s -b "$FJORD_COOKIE" -o /dev/null --max-time 600 -X POST -H "Origin: $FJORD" "$FJORD/api/stacks/t-ajs/down"
  out=$(curl -s -b "$FJORD_COOKIE" --max-time 900 -X POST -H "Origin: $FJORD" "$FJORD/api/stacks/t-ajs/update")
  case $out in *"updated and left stopped"*) say "PASS t-ajs: the update says it left the stack stopped" ;;
    *) say "FAIL t-ajs: the update output does not say it left the stack stopped: $(echo "$out" | tail -2 | tr '\n' ' ')" ;; esac
  case $out in *"[error]"*) say "FAIL t-ajs: the update printed an [error]" ;; esac
  up=$(on 'jls name | grep "^t_ajs_"' | tr '\n' ' ')
  [ -z "$up" ] && say "PASS t-ajs: no jail came up" || say "FAIL t-ajs: the update brought up $up"
  desired=$(curl -s --max-time 10 -b "$FJORD_COOKIE" "$FJORD/api/stacks/t-ajs" | python3 -c 'import json,sys; print(json.load(sys.stdin)["state"]["desired_state"])')
  [ "$desired" = stopped ] && say "PASS t-ajs: desired state still stopped" || say "FAIL t-ajs: desired state is '$desired', want stopped"
  curl -s -b "$FJORD_COOKIE" -o /dev/null --max-time 600 -X POST -H "Origin: $FJORD" "$FJORD/api/stacks/t-ajs/up"
  new=$(on "buildah inspect --type container --format '{{.FromImageID}}' appjail-t_ajs_sync 2>/dev/null")
  if [ -n "$new" ] && [ "$new" != "$UPS_STALE" ]; then say "PASS t-ajs: Start built sync from the new image"
  else say "FAIL t-ajs: after Start sync runs '${new:-nothing}', still the stale image"; fi
  del_stack t-ajs
  left=$(on 'jls name | grep "^t_ajs_"' | tr '\n' ' ')
  [ -z "$left" ] || say "FAIL t-ajs: jails left behind: $left"
}
