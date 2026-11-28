# A counted failure notice on an app, as an unattended job leaves it. No
# browser can make an automatic update fail three nights running, so the
# notice is written into the stack's state the way fjordd records it, then
# read and dismissed through the pages.
main() {
  run_js failure-notices -e STEP=make || return 1
  on 'PY=$(command -v python3 || ls /usr/local/bin/python3.* 2>/dev/null | head -1); [ -d /var/lib/fjord/stacks/t-fn ] && f=/var/lib/fjord/stacks/t-fn/state.json || f=/var/db/fjord/stacks/t-fn/state.json; [ -n "$PY" ] && $PY - "$f" <<PYEOF
import json, os, sys
p = sys.argv[1]
st = json.load(open(p)) if os.path.exists(p) else {"schema_version": 1}
st["failure_notices"] = [{"kind": "auto-update", "count": 3, "firstAt": "2026-11-24T03:00:05Z", "lastAt": "2026-11-26T03:00:07Z", "message": "the app did not stay up after the update"}]
tmp = p + ".tmp"
json.dump(st, open(tmp, "w"))
os.chmod(tmp, 0o600)
os.rename(tmp, p)
PYEOF' || { say "FAIL failure-notices: could not write the notice on the host"; return 1; }
  run_js failure-notices -e STEP=check
}
after() { del_stack t-fn; }
