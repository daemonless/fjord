# The busy reason is read mid-pull, so the pull has to be real.
before() { on 'podman untag ghcr.io/daemonless/openspeedtest:latest >/dev/null 2>&1; true'; }
after() { del_stack t-word; }
