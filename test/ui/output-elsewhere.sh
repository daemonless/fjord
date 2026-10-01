# An action started over the API, watched from a page that did not start it.
# The image is removed first so the update really pulls and the window is
# long enough to open the page in. Never rmi -f: on a host where AppJail
# shares the store, -f removes the jail's layer under a running jail and
# wedges every pull after it (saturn, 2026-10-01).
main() {
  run_js output-elsewhere -e STEP=install
  on 'podman rmi ghcr.io/daemonless/openspeedtest:latest >/dev/null 2>&1; true'
  # Fire the update and leave it running on the host; the page is opened on it.
  on "nohup curl -s -o /dev/null --max-time 900 -X POST -H 'Origin: $FJORD' $FJORD/api/stacks/t-out/update >/dev/null 2>&1 &"
  run_js output-elsewhere -e STEP=watch
}

after() {
  for _ in $(seq 1 40); do curl -s --max-time 10 "$FJORD/api/stacks/t-out" | grep -q '"busy"' || break; sleep 5; done
  del_stack t-out
}
