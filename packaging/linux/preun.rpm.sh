#!/bin/sh
# 0: the package is being removed. 1 or more: an upgrade, whose new %post
# has already run (and restarted fjordd): leave it alone.
if [ "$1" -eq 0 ] && [ -d /run/systemd/system ]; then
  systemctl disable --now fjordd >/dev/null 2>&1 || true
fi
exit 0
