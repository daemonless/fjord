# The busy reason is read mid-pull, so the pull has to be real.
# Removed, not untagged: an untag keeps the layers, the pull only re-tags,
# and the install is over before the page looks. No -f: an image a container
# still uses stays.
before() { on 'podman rmi ghcr.io/daemonless/openspeedtest:latest >/dev/null 2>&1; true'; }
after() { del_stack t-word; }
