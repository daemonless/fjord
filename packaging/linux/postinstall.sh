#!/bin/sh
# After install or upgrade (deb: "configure"; rpm: a count). fjordd already
# enabled is an upgrade: restarted (KillMode=process: the apps keep
# running). Otherwise it is started, and the welcome says where. Not the
# arguments: dpkg passes the old version to a reinstall after a remove too,
# which left fjordd off (Debian, 2026-10-28).
set -e
case "$1" in
  configure | [0-9]*) ;;
  *) exit 0 ;;
esac
# No systemd running (a chroot, an image build): files only.
[ -d /run/systemd/system ] || exit 0

# install-linux.sh's copy (/usr/local/sbin/fjordd and its unit in
# /etc/systemd/system) would win over the package's: its unit overrides
# ours, and /usr/local/sbin comes first in PATH. Moved aside, not deleted.
took=0
unit=/etc/systemd/system/fjordd.service
if [ -f "$unit" ]; then
  if grep -q '^ExecStart=/usr/local/sbin/fjordd$' "$unit" && grep -q '^Description=fjord stack manager$' "$unit"; then
    mv "$unit" "$unit.install-linux"
    [ -f /usr/local/sbin/fjordd ] && mv /usr/local/sbin/fjordd /usr/local/sbin/fjordd.install-linux
    echo "fjord: replaced the copy install-linux.sh put in /usr/local (kept as *.install-linux)"
    took=1
  else
    echo "fjord: $unit is not the one fjord ships and overrides the package's unit; remove it to use the package's"
  fi
fi

systemctl daemon-reload
systemctl enable --now podman.socket >/dev/null 2>&1 || echo "fjord: could not start podman.socket: systemctl status podman.socket"

if [ "$took" = 1 ]; then
  systemctl reenable fjordd >/dev/null 2>&1
  systemctl restart fjordd || echo "fjord: fjordd did not start: journalctl -u fjordd"
  exit 0
fi
if systemctl is-enabled --quiet fjordd 2>/dev/null; then
  systemctl try-restart fjordd || echo "fjord: fjordd did not restart: journalctl -u fjordd"
  exit 0
fi

systemctl enable --now fjordd >/dev/null 2>&1 || { echo "fjord: fjordd did not start: journalctl -u fjordd"; exit 0; }

port=3567
ip=$(hostname -I 2>/dev/null | awk '{print $1}')
sleep 2
echo
echo "fjord is running: http://${ip:-this-host}:$port"
if [ -f /var/db/fjord/setup-code ]; then
  echo "Set up the account there with this setup code: $(cat /var/db/fjord/setup-code)"
fi
if command -v firewall-cmd >/dev/null && [ "$(firewall-cmd --state 2>/dev/null)" = running ] && ! firewall-cmd --query-port=$port/tcp >/dev/null 2>&1; then
  echo "firewalld blocks port $port, so the page answers only on this machine. To open it:"
  echo "  firewall-cmd --permanent --add-port=$port/tcp && firewall-cmd --reload"
elif command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q '^Status: active' && ! ufw status | grep -q "^$port"; then
  echo "ufw blocks port $port, so the page answers only on this machine. To open it:"
  echo "  ufw allow $port/tcp"
fi
echo "fjord is root on this machine and speaks plain HTTP: keep port $port on your LAN."
exit 0
