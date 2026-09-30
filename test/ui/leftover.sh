# A leftover AppJail private network: made by hand with fjord's _priv name
# and no stack, read and deleted through the page, then checked appjail-side.
NET=t-left_priv
UI_ENV="-e NET=$NET"

before() {
  on "appjail network add -d 'fjord ui test' $NET 10.100.9.0/24 >/dev/null 2>&1; appjail network list -Hp name | tr -d '\\t' | grep -qx $NET"
}

after() {
  if on "appjail network list -Hp name | tr -d '\\t' | grep -qx $NET"; then
    echo "FAIL $NET: appjail still lists it"; on "appjail network remove -d $NET >/dev/null 2>&1; true"
  else echo "PASS $NET: appjail no longer lists it"; fi
}
