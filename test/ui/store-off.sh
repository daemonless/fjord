# Every catalog off for the test, back on after. Needs at least one catalog
# that is on; a host with only the local seed has a store to show anyway.
ON=""
before() {
  ON=$(curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/catalogs" | python3 -c '
import json,sys
print(" ".join(c["id"] for c in json.load(sys.stdin) if c.get("enabled") and not c.get("builtin")))')
  [ -n "$ON" ] || { echo "SKIP store-off: no catalog is turned on"; return 1; }
  for c in $ON; do
    curl -s -o /dev/null --max-time 20 -b "$FJORD_COOKIE" -X POST -H "Content-Type: application/json" -H "Origin: $FJORD" "$FJORD/api/catalogs/$c/toggle" -d '{"enabled":false}'
  done
}
after() {
  for c in $ON; do
    curl -s -o /dev/null --max-time 20 -b "$FJORD_COOKIE" -X POST -H "Content-Type: application/json" -H "Origin: $FJORD" "$FJORD/api/catalogs/$c/toggle" -d '{"enabled":true}'
  done
  n=$(curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/catalogs" | grep -o '"enabled":true' | wc -l | tr -d ' ')
  [ "$n" -gt 0 ] && echo "PASS catalogs turned back on ($n)" || echo "FAIL catalogs still off"
}
