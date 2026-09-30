# Apply with an exec session holding the old container (zensical's failure):
# install through the page, hold `podman exec` open, change the port through
# the page, Apply, then check from off-host.
main() {
  run_js apply -e STEP=install
  on 'podman exec -d t-apply_openspeedtest_1 sleep 900 && echo "exec sessions held: $(podman inspect t-apply_openspeedtest_1 --format "{{len .ExecIDs}}")"'
  run_js apply -e STEP=apply
}

after() {
  if answers "http://$IP:3111/" 6; then echo "PASS t-apply: the new port 3111 answers from off-host"
  else echo "FAIL t-apply: http://$IP:3111/ does not answer from off-host"; fi
  del_stack t-apply
}
