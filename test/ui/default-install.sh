# The day-one install on each engine's default network. Whatever that
# network is on this host (netlab: lan-dhcp for podman, appjail's NAT bridge),
# the address fjordd puts on the Open button is checked from off-host (pf
# never redirects a host to itself); then both stacks go, with their data.
after() {
  for s in t-def-podman t-def-appjail; do
    url=$(curl -s --max-time 20 "$FJORD/api/stacks/$s" | python3 -c '
import json,sys
d=json.load(sys.stdin)
env=dict(l.split("=",1) for l in d.get("env","").splitlines() if "=" in l)
# No linkHost = published on this host, which the page reaches by its own address.
host=d.get("linkHost") or sys.argv[1]
print("http://%s:%s/" % (host, env.get("WEB_PORT", "")))' "$IP" 2>/dev/null)
    if [ -z "$url" ]; then echo "FAIL $s: fjordd names no address to open"; continue; fi
    if answers "$url" 12; then echo "PASS $s: answers at $url from off-host"
    else echo "FAIL $s: $url does not answer from off-host"; fi
  done
  del_stack t-def-podman t-def-appjail
}
