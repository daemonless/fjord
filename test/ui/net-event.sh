# A network made or removed elsewhere reaches an open stack page.
after() {
  del_stack t-netev
  del_net t-netev-lan podman
}
