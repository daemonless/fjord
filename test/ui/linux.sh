# fjord on a Linux host (Fedora, Debian): skipped anywhere else. Needs the
# linuxserver catalog among the host's catalogs, and PARENT, the interface a
# macvlan LAN network goes on (netlab-linux: enp0s6f1, on lan4 with DHCP).
UI_ENV="-e PARENT=${PARENT:-enp0s6f1}"
before() {
  [ "$(on uname -s)" = Linux ] || { echo "SKIP linux: $HOST is not a Linux host"; return 1; }
  # Only an SELinux host (Fedora) refuses a folder fjord did not label;
  # Debian runs it. The script is told which it is on.
  UI_ENV="$UI_ENV -e SELINUX=$(on 'test -e /sys/fs/selinux/enforce && cat /sys/fs/selinux/enforce || echo 0')"
  on "rm -rf /srv/t-lx-foreign; mkdir -p /srv/t-lx-foreign"
}
after() {
  # The host cannot reach its own macvlan children: this is checked from here.
  addr=$(curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/stacks/t-lx-mv" | python3 -c '
import json,sys
d=json.load(sys.stdin)
print(next((c["addresses"]["t-mv"] for c in d.get("status",{}).get("containers",[]) if (c.get("addresses") or {}).get("t-mv")), ""))' 2>/dev/null)
  if [ -n "$addr" ] && answers "http://$addr/" 6; then echo "PASS the app on t-mv answers at $addr from off-host"
  else echo "FAIL the app on t-mv does not answer at ${addr:-no address} from off-host"; fi
  del_stack t-lx-app t-lx-own t-lx-foreign t-lx-mv
  del_net t-mv podman
  on "rm -rf /srv/t-lx-foreign"
}
