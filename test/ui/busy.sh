# Busy while installing: the pull has to be real, so the image goes first.
before() { on 'podman untag ghcr.io/daemonless/openspeedtest:latest >/dev/null 2>&1; true'; }
after() { del_stack t-busy; }
