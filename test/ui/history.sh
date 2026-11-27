# The History tab and the System page's History. The delete is read back
# from fjordd: a deleted stack's History stays.
after() {
  del_stack t-hist
  last=$(curl -s --max-time 20 -b "$FJORD_COOKIE" "$FJORD/api/history?limit=20" | python3 -c '
import json,sys
es=[e for e in json.load(sys.stdin) if e["stack"]=="t-hist"]
print(es[0]["action"]+"/"+es[0]["result"] if es else "none")' 2>/dev/null)
  if [ "$last" = "delete/ok" ]; then echo "PASS t-hist: its delete is in History after the stack is gone"
  else echo "FAIL t-hist: History after the delete ends with $last"; fi
}
