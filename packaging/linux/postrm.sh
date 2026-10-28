#!/bin/sh
# After removal. deb: "remove" or "purge"; rpm: 0 on removal (1 or more on
# upgrade). fjord's data stays either way.
[ -d /run/systemd/system ] && systemctl daemon-reload >/dev/null 2>&1
case "$1" in
  remove | 0) echo "fjord: removed. Its apps and data are kept in /var/db/fjord; delete that folder to remove them too." ;;
esac
exit 0
