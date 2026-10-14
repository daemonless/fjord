# Install a stack to open a shell in, read the Shell tab in light mode, delete it.
main() {
  run_js shell-light -e STEP=install
  run_js shell-light
}

after() { del_stack t-shl; }
