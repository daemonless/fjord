# CNI -> netavark -> CNI from the System page, then a reboot. Disruptive:
# podman's whole network setup moves twice. Run it on a VM snapshotted first.

# wait_switch waits (up to 20 minutes) for the switch the page started to
# end. Asked from the host: the switch stops the test's own container.
wait_switch() {
  i=0
  while :; do
    r=$(curl -s --max-time 10 -b "$FJORD_COOKIE" "$FJORD/api/system/netavark" | grep -o '"running":[a-z]*')
    [ "$r" = '"running":false' ] && return 0
    i=$((i + 1))
    [ $i -gt 120 ] && { say "FAIL switch: still running after 20 minutes"; return 1; }
    sleep 10
  done
}
main() {
  run_js netavark-switch -e STEP=setup
  grep -q '^SKIP t-nvs' "$LOG" && return 0
  run_js netavark-switch -e STEP=switch
  wait_switch || return 1
  run_js netavark-switch -e STEP=switched
  run_js netavark-switch -e STEP=onnv
  run_js netavark-switch -e STEP=back
  wait_switch || return 1
  run_js netavark-switch -e STEP=backed
  say "rebooting the host"
  on "shutdown -r now" >/dev/null 2>&1
  sleep 30
  i=0
  until on "fetch -qo /dev/null http://127.0.0.1:3567/healthz" 2>/dev/null; do
    i=$((i + 1))
    [ $i -gt 60 ] && { say "FAIL reboot: the host did not come back in 10 minutes"; return 1; }
    sleep 10
  done
  sleep 60  # start on boot brings the stacks up
  run_js netavark-switch -e STEP=check
}
after() {
  for s in t-nvs-port t-nvs-own t-nvs-pool t-nvs-dhcp t-nvs-new; do del_stack $s; done
  for n in t-nvs-pool t-nvs-dhcp t-nvs-new; do del_net $n podman; done
  on "podman network rm t-nvs-own_inner" >/dev/null 2>&1
}
