# AppJail on host and back: the JS checks the host states (director, this
# host's :3000); here, the bridge it ends on, from off-host. The appjail
# default goes back to what it was before.
before() {
  AJ_DEFAULT=$(curl -s --max-time 10 "$FJORD/api/settings/network" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("forEngine",{}).get("appjail",""))')
}
after() {
  # The JS ends with the stack back on bridge from the Services tab.
  # vnet, not ip4: a vnet jail reports ip4=inherit too.
  vnet=$(on 'jls -j t_ajhost_openspeedtest vnet 2>/dev/null')
  if [ "$vnet" = new ]; then echo "PASS t-ajhost: back on bridge, jail has its own stack (vnet=new)"
  else echo "FAIL t-ajhost: jail vnet=${vnet:-<no jail>} after Save bridge, want new"; fi
  if answers "http://$IP:3000/" 12; then echo "PASS t-ajhost: on bridge, published :3000 answers from off-host"
  else echo "FAIL t-ajhost: on bridge, http://$IP:3000/ does not answer from off-host"; fi
  del_stack t-ajhost
  curl -s -o /dev/null --max-time 10 -X POST -H "Origin: $FJORD" -H 'Content-Type: application/json' \
    -d "{\"engine\":\"appjail\",\"network\":\"$AJ_DEFAULT\"}" "$FJORD/api/settings/network"
}
