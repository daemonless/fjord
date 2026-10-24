#!/bin/sh
# Install fjord on a Linux host (rootful podman): fjordd as a systemd service.
#
#   curl -fsSL https://github.com/daemonless/fjord/releases/latest/download/install-linux.sh | sudo sh
#
# FJORD_VERSION=0.4.0 picks a release (default: the latest); FJORDD_BINARY
# and FJORDD_UNIT install local files instead of downloading them.
set -eu

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'fjord install: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || fail "run as root (sudo sh install-linux.sh)"
command -v systemctl >/dev/null || fail "this installer needs systemd"

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "no fjordd build for $(uname -m)" ;;
esac

step "Checking podman"
missing=""
for c in podman podman-compose conmon; do command -v "$c" >/dev/null || missing="$missing $c"; done
if [ -n "$missing" ]; then
  if command -v dnf >/dev/null; then hint="dnf install -y$missing"; else hint="apt install -y$missing"; fi
  fail "missing:$missing -- install with: $hint"
fi
podman --version

base=https://github.com/daemonless/fjord/releases
if [ -n "${FJORD_VERSION:-}" ]; then url="$base/download/v$FJORD_VERSION"; else url="$base/latest/download"; fi
get() { # get <name> <dest>
  if command -v curl >/dev/null; then curl -fsSL "$url/$1" -o "$2"; else wget -qO "$2" "$url/$1"; fi
}

step "Installing fjordd to /usr/local/sbin"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
if [ -n "${FJORDD_BINARY:-}" ]; then cp "$FJORDD_BINARY" "$tmp/fjordd"; else get "fjordd-linux-$arch" "$tmp/fjordd"; fi
install -m 755 "$tmp/fjordd" /usr/local/sbin/fjordd
command -v restorecon >/dev/null && restorecon /usr/local/sbin/fjordd || true
/usr/local/sbin/fjordd -version

step "Installing the fjordd service"
if [ -n "${FJORDD_UNIT:-}" ]; then cp "$FJORDD_UNIT" "$tmp/fjordd.service"; else get fjordd.service "$tmp/fjordd.service"; fi
install -m 644 "$tmp/fjordd.service" /etc/systemd/system/fjordd.service
systemctl daemon-reload

step "Starting podman's API socket and fjordd"
systemctl enable --now podman.socket
systemctl enable --now fjordd
sleep 2
systemctl is-active --quiet fjordd || fail "fjordd did not start: journalctl -u fjordd"

port=3567
ip=$(hostname -I 2>/dev/null | awk '{print $1}')
if command -v firewall-cmd >/dev/null && [ "$(firewall-cmd --state 2>/dev/null)" = running ] && ! firewall-cmd --query-port=$port/tcp >/dev/null 2>&1; then
  printf '\nfirewalld blocks port %s, so the page answers only on this machine. To open it:\n  firewall-cmd --permanent --add-port=%s/tcp && firewall-cmd --reload\n' "$port" "$port"
elif command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q '^Status: active' && ! ufw status | grep -q "^$port"; then
  printf '\nufw blocks port %s, so the page answers only on this machine. To open it:\n  ufw allow %s/tcp\n' "$port" "$port"
fi

cat <<MSG

fjord is running: http://${ip:-this-host}:$port

Set up the account there with this setup code:
  $(cat /var/db/fjord/setup-code 2>/dev/null || echo "(none: the account exists; forgot it? fjordd passwd)")

fjord is root on this machine and speaks plain HTTP: keep port $port on
your LAN; never forward it to the internet.
MSG
