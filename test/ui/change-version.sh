# Change Version with the page gone, then with it open: the stack is made
# by the test on 2.0.5 and ends there again; the container's own image is
# the proof, the API's view of it was checked along the way.
main() {
  run_js change-version -e STEP=gone
  run_js change-version -e STEP=stays
}
after() {
  img=$(on "podman inspect t-cv_speed_1 --format '{{.ImageName}}'" 2>/dev/null)
  case "$img" in
    *openspeedtest:2.0.5) echo "PASS t-cv: the container runs $img" ;;
    *) echo "FAIL t-cv: the container runs ${img:-nothing}, not 2.0.5" ;;
  esac
  del_stack t-cv
}
