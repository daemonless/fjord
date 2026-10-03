# Choices: the install answers the Database choice with PostgreSQL. The app
# has to reach the database it brought, which the page cannot see: check the
# tables exist, and that the app answers from off-host. Needs a catalog that
# carries x-fjord.choices for vikunja (CHOICE_CATALOG, default "proof").
before() {
  curl -s --max-time 20 "$FJORD/api/catalogs" | grep -q "\"${CHOICE_CATALOG:-proof}\"" || { echo "no '${CHOICE_CATALOG:-proof}' catalog on this host"; return 1; }
}
UI_ENV="-e CHOICE_CATALOG=${CHOICE_CATALOG:-proof}"
after() {
  n=$(on "podman exec t-choice_postgres_1 psql -U vikunja -d vikunja -tAc \"select count(*) from information_schema.tables where table_schema='public'\"" 2>/dev/null | tr -d '[:space:]')
  if [ "${n:-0}" -gt 0 ] 2>/dev/null; then echo "PASS t-choice: vikunja made $n tables in the postgres it brought"
  else echo "FAIL t-choice: no tables in postgres (${n:-none})"; fi
  if answers "http://$IP:3461/api/v1/info" 12; then echo "PASS t-choice: answers on $IP:3461 from off-host"
  else echo "FAIL t-choice: $IP:3461 does not answer from off-host"; fi
  del_stack t-choice
}
