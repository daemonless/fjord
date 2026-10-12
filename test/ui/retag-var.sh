# Change Version on a variable-tagged image: the stack is made by the test.
# The page shows the compose's tag; the container's own image is the proof.
after() {
  img=$(on "podman inspect t-var_speed_1 --format '{{.ImageName}}'" 2>/dev/null)
  case "$img" in
    *openspeedtest:2.0.5) echo "PASS t-var: the container runs $img" ;;
    *) echo "FAIL t-var: the container runs ${img:-nothing}, not 2.0.5" ;;
  esac
  del_stack t-var
}
