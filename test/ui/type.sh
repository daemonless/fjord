# Type (networks / host / none): the published port is checked from off-host.
after() {
  if answers "http://$IP:3106/" 6; then echo "PASS t-tc: published port 3106 answers through the bridge"
  else echo "FAIL t-tc: http://$IP:3106/ does not answer from off-host"; fi
  del_stack t-tc t-tcj
  del_net t-lan podman
}
