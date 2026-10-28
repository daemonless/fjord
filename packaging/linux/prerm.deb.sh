#!/bin/sh
# Before removal ("remove"), not before an upgrade ("upgrade"): the new
# package's postinst restarts fjordd then.
set -e
if [ "$1" = remove ] && [ -d /run/systemd/system ]; then
  systemctl disable --now fjordd >/dev/null 2>&1 || true
fi
exit 0
