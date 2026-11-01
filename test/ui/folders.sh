# Folder defaults follow the stack name; a folder inside another stack's is
# refused. Nothing is installed; this only confirms no folder appeared.
# t-own is the other stack, saved by the test; t-lnms only exists if the
# refusal failed.
after() {
  if on 'test -e /containers/t-lnms'; then echo "FAIL t-lnms: a folder was made for a refused install"
  else echo "PASS t-lnms: no folder made for the refused install"; fi
  del_stack t-own t-lnms
}
