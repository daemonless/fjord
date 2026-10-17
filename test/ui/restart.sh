# The restart policy through the page, judged by what podman does after a
# crash: the app's process killed from the host (exit 137), not `podman
# kill`, which podman counts as a stop by the user.
C=t-restart_openspeedtest_1
# podman 5.8.6 on FreeBSD never restarts a crashed container that publishes a
# port: its cleanup cannot bind the port again ("address already in use").
# What fjord writes is checked either way; the crash itself is podman's.
PODMAN_BUG='podman does not restart a crashed container with a published port (bind: address already in use; podman bug, not fjord)'

# crash kills the container's process; prints its state and restart count
# after 15 s.
crash() {
  on "kill -9 \$(podman inspect $C --format '{{.State.Pid}}') && sleep 15 && podman inspect $C --format '{{.State.Status}} {{.RestartCount}}'"
}

main() {
  run_js restart -e STEP=install
  run_js restart -e STEP=set -e POLICY=no
  s=$(crash)
  case $s in exited*) say "PASS no: a crashed app stays down ($s)" ;; *) say "FAIL no: after a crash it is $s" ;; esac
  run_js restart -e STEP=set -e POLICY=unless-stopped
  s=$(crash)
  case $s in running*) say "PASS unless-stopped: a crashed app comes back ($s)" ;; exited*) say "SKIP unless-stopped: after a crash it is $s -- $PODMAN_BUG" ;; *) say "FAIL unless-stopped: after a crash it is $s" ;; esac
  run_js restart -e STEP=set -e POLICY=on-failure
  p=$(on "podman inspect $C --format '{{.HostConfig.RestartPolicy.Name}}:{{.HostConfig.RestartPolicy.MaximumRetryCount}}'")
  [ "$p" = on-failure:5 ] && say "PASS on-failure: podman has $p" || say "FAIL on-failure: podman has $p"
  s=$(crash)
  case $s in running*) say "PASS on-failure: a crashed app comes back ($s)" ;; exited*) say "SKIP on-failure: after a crash it is $s -- $PODMAN_BUG" ;; *) say "FAIL on-failure: after a crash it is $s" ;; esac
}

after() {
  del_stack t-restart t-restart-aj
}
