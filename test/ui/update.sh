# An update through the panel: syncthing 2.1.3 -> 2.1.5 on the bridge. The
# published port is checked from off-host after the update; then the stack goes.
after() {
  # The update's health watch outlives the click by half a minute: a delete
  # while it runs is refused, and the stack would be left behind.
  for _ in $(seq 1 30); do curl -s --max-time 10 "$FJORD/api/stacks/t-upd" | grep -q '"busy"' || break; sleep 5; done
  if answers "http://$IP:3122/" 12; then echo "PASS t-upd: answers on $IP:3122 from off-host after the update"
  else echo "FAIL t-upd: http://$IP:3122/ does not answer from off-host after the update"; fi
  del_stack t-upd
}
