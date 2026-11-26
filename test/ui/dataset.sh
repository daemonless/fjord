# /containers as a ZFS dataset (zroot/containers) and fjord's first App
# data location while the test runs; both put back afterwards.
LOCS_FILE=/tmp/fjord-ui-dataset-locations.json
before() {
  on "zfs list zroot/containers >/dev/null 2>&1 || { zfs create -o mountpoint=/containers zroot/containers && touch /tmp/fjord-ui-dataset-made; }"
  curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/settings/storage" > "$LOCS_FILE"
  locs=$(python3 -c 'import json,sys; l=json.load(open(sys.argv[1])).get("locations") or []; print(json.dumps({"locations": ["/containers"] + [x for x in l if x != "/containers"]}))' "$LOCS_FILE")
  curl -s -o /dev/null --max-time 20 -b "$FJORD_COOKIE" -H "Origin: $FJORD" -H 'Content-Type: application/json' -d "$locs" "$FJORD/api/settings/storage"
}

after() {
  if on "! zfs list zroot/containers/t-ds >/dev/null 2>&1 && [ ! -e /containers/t-ds ]"; then
    echo "PASS t-ds: its dataset and folder are gone"
  else echo "FAIL t-ds: zroot/containers/t-ds or /containers/t-ds is still there"; fi
  if on "zfs list zroot/containers >/dev/null 2>&1"; then
    echo "PASS zroot/containers: the parent was not touched"
  else echo "FAIL zroot/containers: the parent is gone"; fi
  del_stack t-ds
  on "zfs destroy -r zroot/containers/t-ds >/dev/null 2>&1; true"
  locs=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); l=d.get("locations") or []; print(json.dumps({"locations": [] if l == [d.get("default")] else l}))' "$LOCS_FILE" 2>/dev/null)
  [ -n "$locs" ] && curl -s -o /dev/null --max-time 20 -b "$FJORD_COOKIE" -H "Origin: $FJORD" -H 'Content-Type: application/json' -d "$locs" "$FJORD/api/settings/storage"
  on "[ -f /tmp/fjord-ui-dataset-made ] && zfs destroy zroot/containers && rm -f /tmp/fjord-ui-dataset-made; true"
  rm -f "$LOCS_FILE"
}
