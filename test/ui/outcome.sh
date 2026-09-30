# A failed install is said on the page and in fjordd's log.
after() {
  if on 'grep -q "install t-fail failed" /var/log/fjordd.log'; then echo "PASS t-fail: fjordd.log says the install failed"
  else echo "FAIL t-fail: nothing about the failed install in fjordd.log"; fi
  del_stack t-fail
}
