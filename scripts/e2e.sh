#!/bin/sh
# Run fjord's end-to-end suite (test/e2e) against a live fjordd, and check it
# left nothing behind.
#
#   scripts/e2e.sh <host> [pattern]
#
#   host     ssh destination running fjordd (root@192.168.4.103), or "local"
#   pattern  run only tests matching it (go test -run), e.g. Address
#
# The suite drives the host's real podman and fjordd: point it at a disposable
# VM or a dev host, never at one whose stacks matter.
#
# Settings are passed through to the tests when set:
#   E2E_LAN        a pool network to test addresses on (default lan)
#   E2E_LAN_SPARE  an unused address in its range (default 192.168.86.249)
#   E2E_DHCP       a DHCP network (default lan-dhcp; its test skips without one)
#   E2E_SUDO       how the tests run podman (default: "" as root, else doas)
set -eu

usage() { sed -n '4,9p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
[ $# -ge 1 ] || usage
host=$1
pattern=${2:-}
cd "$(dirname "$0")/.."

step() { printf '\n==> %s\n' "$*"; }
fail() { printf '\n!!  %s\n' "$*" >&2; exit 1; }

# on runs a command on the target host.
on() {
	if [ "$host" = local ]; then sh -c "$1"; else ssh -o BatchMode=yes "$host" "$1"; fi
}

step "Checking fjordd on $host"
version=$(on 'curl -fsS localhost:3567/api/about' | sed -n 's/.*"version":"\([^"]*\)".*/\1/p') ||
	fail "fjordd does not answer on $host:3567 -- start it first"
echo "fjordd $version"

step "Building the test binary"
bin=$(mktemp -t fjord-e2e)
trap 'rm -f "$bin"' EXIT
go test -c -tags e2e -o "$bin" ./test/e2e

step "Running the suite${pattern:+ (tests matching $pattern)}"
if [ "$host" = local ]; then
	remote=$bin
else
	remote=/tmp/fjord-e2e.test
	scp -q "$bin" "$host:$remote"
fi
# As root the tests need no doas; as anyone else they do.
user=$(on 'id -u')
sudo_default=doas
[ "$user" = 0 ] && sudo_default=
envs="E2E_SUDO='${E2E_SUDO-$sudo_default}'"
for v in E2E_LAN E2E_LAN_SPARE E2E_DHCP E2E_FJORD_ROOT; do
	eval "val=\${$v-}"
	[ -n "$val" ] && envs="$envs $v='$val'"
done
log=$(mktemp -t fjord-e2e-log)
set +e
on "cd /tmp && env $envs $remote -test.v -test.count=1 -test.timeout 30m ${pattern:+-test.run '$pattern'}" >"$log" 2>&1
rc=$?
set -e
[ "$host" = local ] || on "rm -f $remote"

step "Results"
grep -E '^(--- (PASS|FAIL|SKIP)|PASS$|FAIL$|panic:)' "$log" || true
if [ $rc -ne 0 ]; then
	echo
	echo "Failures, with their output:"
	grep -E -B1 -A12 '^--- FAIL' "$log" | grep -v '^--- PASS' || tail -40 "$log"
fi
echo "(full log: $log)"

step "Checking for leftovers"
left=$(on "
	p=podman; [ \$(id -u) = 0 ] || p='doas podman'
	\$p ps -a --format '{{.Names}}' | grep '^e2e' | sed 's/^/container /'
	ls /var/db/fjord/stacks 2>/dev/null | grep '^e2e' | sed 's/^/stack /'
	ls /containers 2>/dev/null | grep '^e2e' | sed 's|^|folder /containers/|'
	for f in /var/run/cni/networks/*/*; do
		[ -f \"\$f\" ] || continue
		head -1 \"\$f\" 2>/dev/null | grep -q '^e2e' && echo \"reservation \$f\"
	done
	true")
if [ -n "$left" ]; then
	echo "$left"
	fail "the suite left the above behind"
fi
echo "none"

[ $rc -eq 0 ] || fail "some tests failed (see above)"
step "All passed on $host (fjordd $version)"
