# Newer syncthing versions as first seen three days ago, so a version bump
# has soaked (fjordd keeps the table in memory: written with it stopped),
# and the update settings put back afterwards. needs: freebsd
before() {
  _sf=/var/db/fjord/update-first-seen.json
  on "service fjordd stop >/dev/null; cp $_sf /tmp/fjord-ui-au-seen.json 2>/dev/null || echo '{}' > /tmp/fjord-ui-au-seen.json; true"
  # The offer is made once per host: made again for the test, put back after.
  on "cp /var/db/fjord/settings.json /tmp/fjord-ui-au-settings.json"
  _cfg=$(on "cat /tmp/fjord-ui-au-settings.json" | python3 -c 'import json,sys; d=json.load(sys.stdin); d.pop("autoUpdateOffered", None); print(json.dumps(d))') || return 1
  printf '%s\n' "$_cfg" | on "cat > /var/db/fjord/settings.json"
  _seen=$(on "cat /tmp/fjord-ui-au-seen.json" | python3 -c '
import json, sys, datetime
try: t = json.load(sys.stdin)
except Exception: t = {}
old = (datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(days=3)).strftime("%Y-%m-%dT%H:%M:%SZ")
for minor in range(1, 10):
    for patch in range(0, 40):
        t["ghcr.io/daemonless/syncthing:2.%d.%d" % (minor, patch)] = old
print(json.dumps(t))
') || return 1
  printf '%s\n' "$_seen" | on "cat > $_sf"
  on "service fjordd start >/dev/null; sleep 3"
  # A stack from before Keep up to date existed: installed with no answer.
  _m=$(curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/catalog/daemonless/manifests/openspeedtest.yaml")
  python3 -c 'import json,sys; print(json.dumps({"name":"t-old","app_id":"openspeedtest","manifest":sys.stdin.read(),"values":{"WEB_PORT":"3126"},"engine":"podman"}))' <<EOM | \
    curl -s -o /dev/null --max-time 600 -b "$FJORD_COOKIE" -H "Origin: $FJORD" -H 'Content-Type: application/json' -X POST -d @- "$FJORD/api/apps/install"
$_m
EOM
  # A bad update, simulated: once t-au2 runs anything newer than 2.1.3, its
  # container is stopped (cleanly: a kill mid-start can corrupt syncthing's
  # database, and then no version starts) inside the health watch, once.
  on "rm -f /tmp/fjord-ui-au-killed; cat > /tmp/fjord-ui-au-killer.sh" <<'KILLER'
i=0
while [ $i -lt 1500 ]; do
  c=$(podman ps --format '{{.Names}} {{.Image}}' | awk '$1 ~ /^t-au2[_-]/ && $2 !~ /:2\.1\.3$/ {print $1; exit}')
  if [ -n "$c" ]; then sleep 12; podman stop -t 20 "$c" >/dev/null 2>&1; echo "$c" > /tmp/fjord-ui-au-killed; exit 0; fi
  i=$((i+1)); sleep 1
done
KILLER
  on "nohup sh /tmp/fjord-ui-au-killer.sh >/dev/null 2>&1 &"
}
after() {
  for _ in $(seq 1 30); do curl -s --max-time 10 -b "$FJORD_COOKIE" "$FJORD/api/stacks/t-au" | grep -q '"busy"' || break; sleep 5; done
  if on "grep -q 'auto-update: ' /var/log/fjordd.log"; then
    echo "PASS fjordd logged its runs: $(on "grep 'auto-update: ' /var/log/fjordd.log | tail -2 | cut -c21- | tr '\n' '|'")"
  else echo "FAIL fjordd logged no auto-update run"; fi
  on "pkill -f fjord-ui-au-killer.sh; rm -f /tmp/fjord-ui-au-killer.sh /tmp/fjord-ui-au-killed; true"
  on "c=\$(podman ps -a --format '{{.Names}}' | grep '^t-au2' | head -1); [ -n \"\$c\" ] && podman logs --tail 15 \$c 2>&1" | sed 's/^/  t-au2 log: /' | cut -c1-200
  for _st in t-au t-au2 t-old; do
    for _i in 1 2 3 4 5 6 7 8 9 10 11 12; do
      del_stack $_st
      curl -s -o /dev/null -w '%{http_code}' --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/stacks/$_st" | grep -q 404 && break
      sleep 10
    done
  done
  curl -s -o /dev/null --max-time 20 -b "$FJORD_COOKIE" -H "Origin: $FJORD" -X POST "$FJORD/api/autoupdate/resume"
  # The window, the offer and the first-seen table as they were.
  on "service fjordd stop >/dev/null; cp /tmp/fjord-ui-au-seen.json /var/db/fjord/update-first-seen.json; cp /tmp/fjord-ui-au-settings.json /var/db/fjord/settings.json; rm -f /tmp/fjord-ui-au-seen.json /tmp/fjord-ui-au-settings.json; service fjordd start >/dev/null; true"
}
