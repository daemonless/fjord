# The color startup banner of daemonless images: every daemonless service
# fjord installs gets DAEMONLESS_BANNER=color, on both engines, and a value
# the user writes into .env stands. banner.js installs t-ban-podman and
# t-ban-appjail from the store, reads the Logs tab, and sets the podman one
# to plain from the .env tab; here the containers' own environment is read.
after() {
  env=$(on "podman inspect t-ban-podman_zensical_1 --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null | grep '^DAEMONLESS_BANNER='")
  if [ "$env" = "DAEMONLESS_BANNER=plain" ]; then echo "PASS t-ban-podman: the container has the value the user set ($env)"
  else echo "FAIL t-ban-podman: container env '${env:-<no DAEMONLESS_BANNER>}', want DAEMONLESS_BANNER=plain"; fi
  jail=$(on "jls -N name 2>/dev/null | grep '^t_ban_appjail'" | head -n 1)
  if [ -z "$jail" ]; then echo "FAIL t-ban-appjail: no jail running"
  else
    set_=$(on "appjail oci get-env $jail DAEMONLESS_BANNER 2>/dev/null")
    pid=$(on "ps -J $jail -o pid= 2>/dev/null" | awk 'NR==1{print $1}')
    live=$(on "procstat -e $pid 2>/dev/null" | grep -o 'DAEMONLESS_BANNER=[a-z]*' | head -n 1)
    if [ "$set_" = color ] && [ "$live" = "DAEMONLESS_BANNER=color" ]; then echo "PASS t-ban-appjail: the jail's environment has DAEMONLESS_BANNER=color (set and live)"
    else echo "FAIL t-ban-appjail: jail $jail set '${set_:-<none>}', process env '${live:-<none>}', want color"; fi
  fi
  del_stack t-ban-podman t-ban-appjail
}
