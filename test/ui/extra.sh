# Setup, a multi-service stack on both engines (paperless-ngx) and Adopt.
# ONLY=setup,adopt picks parts. paperless pulls several GB; a full pool
# wedged podman mid-run once, so the run refuses to start without room.
ONLY=${ONLY:-setup,pl,pj,adopt}
MIN_GB=${MIN_GB:-8}
UI_ENV="-e ONLY=$ONLY -e KEEP=t-adopt"

before() {
  free=$(on 'zfs list -Hpo avail zroot' 2>/dev/null)
  free_gb=$(( ${free:-0} / 1073741824 ))
  if [ "$free_gb" -lt "$MIN_GB" ]; then
    echo "$HOST has ${free_gb} GB free, this test needs ${MIN_GB} GB (MIN_GB=... to change)"
    return 1
  fi
  case ",$ONLY," in *,adopt,*)
    # Something to adopt: a container started by hand, outside fjord.
    on 'podman rm -f t-adopt >/dev/null 2>&1; podman run -d --name t-adopt -p 3102:3000 ghcr.io/daemonless/openspeedtest:latest >/dev/null' ;;
  esac
}

after() {
  case ",$ONLY," in *,adopt,*)
    if answers "http://$IP:3102/"; then echo "PASS t-adopt: 3102 answers from off-host after adopting"
    else echo "FAIL t-adopt: http://$IP:3102/ does not answer from off-host"; fi
    del_stack t-adopt
    on 'podman rm -f t-adopt >/dev/null 2>&1; true' ;;
  esac
}
