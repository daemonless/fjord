# The network matrix. SETTLE_MS>0 only to get past the Networks refresh race
# on a build without the fix; 0 clicks as fast as the page lets you.
UI_ENV="-e SETTLE_MS=${SETTLE_MS:-0} -e KEEP=t-br"

after() {
  if answers "http://$IP:3101/"; then echo "PASS t-br: published port 3101 answers from off-host"
  else echo "FAIL t-br: http://$IP:3101/ does not answer from off-host"; fi
  del_stack t-br
}
