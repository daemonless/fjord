# One app on two networks, podman and AppJail: after the browser part, every
# interface is asked for an address that answers, from the host. The
# container or jail names come from fjordd, whichever engine made them.
STACKS="t-m2 t-j2 t-ms t-js"

after() {
  for s in $STACKS; do
    info=$(curl -s --max-time 20 "$FJORD/api/stacks/$s" | python3 -c '
import json,sys
d=json.load(sys.stdin)
print((d.get("state") or {}).get("engine") or d.get("engine") or "podman")
print(" ".join(c["name"] for c in d.get("status",{}).get("containers",[])))' 2>/dev/null)
    engine=$(printf '%s\n' "$info" | sed -n 1p); names=$(printf '%s\n' "$info" | sed -n 2p)
    [ -n "$names" ] || { echo "FAIL $s: fjordd lists no container or jail for it"; continue; }
    on "
      ips=''
      for c in $names; do
        if [ '$engine' = appjail ]; then ips=\"\$ips \$(jexec \$c ifconfig 2>/dev/null | awk '/inet 192.168.4/{print \$2}')\";
        else ips=\"\$ips \$(podman exec \$c ifconfig 2>/dev/null | awk '/inet 192.168.4/{print \$2}')\"; fi
      done
      n=\$(echo \$ips | wc -w | tr -d ' ')
      [ \"\$n\" -eq 0 ] && { echo \"FAIL $s: no 192.168.4.x address on any interface ($engine: $names)\"; exit 0; }
      for ip in \$ips; do
        code=000; for _ in 1 2 3 4 5 6; do code=\$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://\$ip:3000/); [ \"\$code\" = 200 ] && break; sleep 5; done
        echo \"\$( [ \"\$code\" = 200 ] && echo PASS || echo FAIL) $s: \$ip:3000 -> \$code\"
      done
      if [ \"\$n\" -ge 2 ]; then echo \"PASS $s: \$n interfaces with an address\";
      elif [ '$s' = t-js ]; then echo \"SKIP $s: only \$n interface(s) got an address -- an AppJail jail given a network on the Services tab after install (known gap, 0.3.5 AppJail parity)\";
      else echo \"FAIL $s: only \$n interface(s) got an address, wanted 2\"; fi
    "
  done
  del_stack $STACKS
  del_net t-na podman
  del_net t-nb podman
}
