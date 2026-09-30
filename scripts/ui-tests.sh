#!/bin/sh
# Run fjord's UI tests (test/ui) against a live fjordd, through the real
# pages, and check they left nothing behind.
#
#   scripts/ui-tests.sh <host> [test...]
#
#   host   ssh destination running fjordd (root@192.168.4.103)
#   test   names from test/ui (open-link, wording, ...); default: the whole
#          suite in order, without the opt-in ones (see OPT_IN below)
#
# A test is test/ui/<name>.js, run in ghcr.io/daemonless/playwright ON THE
# HOST with --network host, so 127.0.0.1:3567 is fjordd. An optional
# test/ui/<name>.sh beside it, sourced here, adds what a browser cannot do:
#   before()   set the host up (an image to remove so a pull is real, a
#              network made by hand); return 1 to skip the test
#   after()    check from off-host and clean up (a published port must be
#              reached from another machine: pf never redirects a host to
#              itself), print PASS/FAIL lines like the script does
#   main()     run the script itself, for a test that runs it more than once;
#              its own PASS/FAIL lines go through say, so they are counted
#   UI_ENV     extra -e settings for the container
# Helpers for them: on, answers, del_stack, del_net, run_js, say (below).
#
# Screenshots land in test/ui/out/<name>/. Exits 1 if any line failed or
# anything named t-* is left on the host afterwards.
#
# The suite drives the host's real podman, appjail and fjordd: point it at a
# disposable VM, never at a host whose stacks matter. test/ui/README.md says
# what the host needs (bridges, networks, free space).
set -u

usage() { sed -n '4,9p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
[ $# -ge 1 ] || usage
HOST=$1
shift
cd "$(dirname "$0")/.."

IP=${HOST#*@}
FJORD="http://$IP:3567"
REMOTE=/root/fjord-ui-tests
IMAGE=ghcr.io/daemonless/playwright:latest
OUT=test/ui/out

# The default order: cheap and read-only first, the long installs last.
SUITE="open-link keys wording busy folders outcome arch retag-var leftover appjail-dns privdel apply wire type matrix multi extra"
# Opt-in: image-specific, or needing something the default host lacks.
OPT_IN="lnms-admin"

step() { printf '\n==> %s\n' "$*"; }
# on runs a command on the host.
on() { ssh -o BatchMode=yes "$HOST" "$@"; }
# answers <url> [tries] succeeds when the url answers 200 from HERE, off-host.
answers() {
  _tries=${2:-12}
  while [ "$_tries" -gt 0 ]; do
    [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$1")" = 200 ] && return 0
    _tries=$((_tries - 1)); sleep 5
  done
  return 1
}
# del_stack <name>... deletes stacks with their data through fjordd.
del_stack() { for _s in "$@"; do curl -s -o /dev/null --max-time 180 -X DELETE -H "Origin: $FJORD" "$FJORD/api/stacks/$_s?data=1"; done; }
# say prints a line and keeps it in the test's log, where PASS/FAIL are
# counted (after()'s output is already kept; main() has to use this).
say() { echo "$*" | tee -a "$LOG"; }
# del_net <name> <engine> deletes a network through fjordd.
del_net() { curl -s -o /dev/null --max-time 60 -X DELETE -H "Origin: $FJORD" "$FJORD/api/networks/$1?engine=$2"; }
# run_js <name> [-e K=V ...] runs the script on the host; output goes to the
# terminal and the test's log.
run_js() {
  _js=$1; shift
  on "cd $REMOTE && podman run --rm --network host $* \
    -v \$PWD/$_js.js:/app/t.js:ro -v \$PWD/out:/out \
    $IMAGE node /app/t.js 2>&1 | grep -v -E '^\[(usermod|init)\]'" | tee -a "$LOG"
}

tests=$*
[ -n "$tests" ] || tests=$SUITE
for t in $tests; do [ -f "test/ui/$t.js" ] || { echo "no test/ui/$t.js" >&2; exit 2; }; done

step "Copying test/ui to $HOST:$REMOTE"
on "mkdir -p $REMOTE/out" || exit 1
scp -q test/ui/*.js "$HOST:$REMOTE/" || exit 1
on "podman image exists $IMAGE" || { echo "$IMAGE is not on $HOST: podman pull it first" >&2; exit 1; }
rm -rf "$OUT"; mkdir -p "$OUT"

pass=0; fail=0; skipped=""; failed=""
for t in $tests; do
  step "$t"
  LOG="$OUT/$t.log"; : > "$LOG"
  unset -f before after main 2>/dev/null; UI_ENV=""
  [ -f "test/ui/$t.sh" ] && . "test/ui/$t.sh"
  on "rm -f $REMOTE/out/*.png"
  if command -v before >/dev/null 2>&1 && ! before; then
    echo "SKIP $t"; skipped="$skipped $t"; continue
  fi
  if command -v main >/dev/null 2>&1; then main; else run_js "$t" $UI_ENV; fi
  if command -v after >/dev/null 2>&1; then after | tee -a "$LOG"; fi
  mkdir -p "$OUT/$t" && scp -q "$HOST:$REMOTE/out/*.png" "$OUT/$t/" 2>/dev/null
  p=$(grep -c '^PASS' "$LOG"); f=$(grep -c '^FAIL' "$LOG")
  pass=$((pass + p)); fail=$((fail + f))
  [ "$f" -gt 0 ] && failed="$failed $t"
  printf '%s: %s passed, %s failed\n' "$t" "$p" "$f"
done

step "Leftovers on $HOST"
left=$(on 'c=$(podman ps -a --format "{{.Names}}" | grep "^t-"); n=$(podman network ls --format "{{.Name}}" | grep "^t-"); j=$(jls name 2>/dev/null | grep "^t-"); echo "$c $n $j" | tr -s " \n" " "' | sed 's/^ *//;s/ *$//')
if [ -n "$left" ]; then echo "FAIL leftovers: $left"; fail=$((fail + 1)); else echo "PASS nothing named t-* left behind"; fi

step "Summary"
printf '%s passed, %s failed' "$pass" "$fail"
[ -n "$failed" ] && printf ' (in:%s)' "$failed"
[ -n "$skipped" ] && printf '; skipped:%s' "$skipped"
printf '\nScreenshots and logs: %s/\n' "$OUT"
[ "$fail" -eq 0 ]
