# Opt-in: LibreNMS's admin password honoured as given. Tests the librenms
# IMAGE as much as fjord, and needs one built after 2026-09-30 on the host.
after() {
  on 'podman logs t-lnms_librenms_1 2>&1 | grep -q "created admin user admin"' \
    && echo "PASS t-lnms: the init step created the admin" || echo "FAIL t-lnms: no 'created admin user' line in the log"
  on 'podman exec t-lnms_librenms_1 sh -c "cd /usr/local/www/librenms && s6-setuidgid bsd ./lnms config:get password.uncompromised; s6-setuidgid bsd ./lnms config:get password.min_length" 2>/dev/null | tr "\n" " " | grep -q "^true 8 "' \
    && echo "PASS t-lnms: LibreNMS's own password rules are back (true, 8)" || echo "FAIL t-lnms: password settings not restored"
  del_stack t-lnms
}
