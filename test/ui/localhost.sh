# Changes the host (pf's rules, net.pf.filter_local), so it is not in the
# suite: run it by name on a host set up for it. LOCALHOST_PHASE=again is the
# second run, after a reboot; the stack is kept between the two.
UI_ENV="-e PHASE=${LOCALHOST_PHASE:-}"
after() { [ "${LOCALHOST_PHASE:-}" = again ] && del_stack t-local; return 0; }
