# An action started over the API, watched from a page that did not start it.
# The image is removed first so the update really pulls and the window is
# long enough to open the page in. untag, not rmi: rmi refuses an image a
# container uses (t-out's), so nothing was pulled and the update was over
# before the page looked; rmi -f removed a running jail's layer on a host
# where AppJail shares the store (saturn, 2026-10-01). untag only drops the
# name, so the update has to pull, and deletes nothing.
main() {
  run_js output-elsewhere -e STEP=install
  on 'podman untag ghcr.io/daemonless/openspeedtest:latest >/dev/null 2>&1; true'
  # Fire the update from here, in the background: the host may have no curl
  # (netlab lost it with the packages), and a failed POST there was silent.
  ( curl -s -o /dev/null --max-time 900 -X POST -H "Origin: $FJORD" "$FJORD/api/stacks/t-out/update" & )
  run_js output-elsewhere -e STEP=watch
}

after() {
  for _ in $(seq 1 40); do curl -s --max-time 10 "$FJORD/api/stacks/t-out" | grep -q '"busy"' || break; sleep 5; done
  del_stack t-out
}
