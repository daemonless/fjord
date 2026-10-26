# UI tests

Everything through the real pages, judged by the outcome: the containers,
the addresses, what answers. Every bug found by hand becomes one of these.

    scripts/ui-tests.sh root@192.168.4.103            # the suite
    scripts/ui-tests.sh root@192.168.4.103 wording    # one test
    ONLY=setup,adopt scripts/ui-tests.sh root@192.168.4.103 extra
    OFFHOST=pluto scripts/ui-tests.sh local busy            # this machine

`local` runs against the machine itself (podman through doas); `OFFHOST`
names an ssh destination the published-port checks run from, since pf never
redirects a host's own published port back to it.

fjord's sign-in: the runner signs in once as `FJORD_USER` (default `admin`)
with `FJORD_PASSWORD`, every test's browser starts with that session
(`session.js`: `newContext(signedIn({...}))` in a new test), and it signs
out at the end:

    FJORD_PASSWORD=... scripts/ui-tests.sh root@192.168.4.103

Each test is `<name>.js`, a Playwright script run in
`ghcr.io/daemonless/playwright` on the host itself (`--network host`, so
`127.0.0.1:3567` is fjordd). `<name>.sh` beside it, when there is one, does
what a browser cannot: sets the host up (`before`), checks from off-host and
cleans up (`after`), or runs the script more than once (`main`). The runner
sources it; `scripts/ui-tests.sh` documents the helpers.

Output: `PASS`/`FAIL` lines, screenshots and logs under `out/<name>/`
(ignored by git), a leftovers check for anything named `t-*`, exit 1 on any
failure.

## The host

A disposable VM with fjordd, podman, AppJail and the playwright image
pulled. Never a host whose stacks matter: the tests install, recreate and
delete real stacks. netlab (`root@192.168.4.103`, a bhyve VM on pluto) is
the one these were written against; the fixtures they expect:

| what | why |
|---|---|
| `lanbridge` with no host address, DHCP on its wire | the wire probe, DHCP installs |
| `lanbridge2` with a host address | a subnet the form can fill in |
| `v6lab`, a bridge where nothing answers DHCPv4 | the "could not check it" path |
| `lan-dhcp`, a DHCP network on `lanbridge` | the default network for installs |
| catalog with openspeedtest, paperless-ngx, librenms | the apps installed |
| 8 GB free for `extra` | paperless pulls several GB |

Addresses fixed in the scripts (`192.168.4.220-228`) must be free on that
wire.

## The tests

| name | what it proves | stacks |
|---|---|---|
| open-link | where every stack's Open link points (read-only) | |
| keys | the keyboard reaches what was mouse-only: a store card opens on Enter, the stack name is a button, the sashes are sliders (read-only) | |
| default-install | the day-one path: install from the store with nothing changed but the name, on podman and on AppJail, on the engine's default network; the Open address answers from off-host | t-def-podman, t-def-appjail |
| update | an update through the panel, end to end: syncthing 2.1.3 installed, the panel's sentence with a real package diff from the registry, Update taken, the container replaced and running 2.1.5, the health watch clean, Roll back offered, the port answering from off-host | t-upd |
| wording | the wizard says what "private" is; a busy stack says why its buttons are grey | t-word |
| busy | installing reads as busy, not a problem; a second up is refused | t-busy |
| output-elsewhere | an update started over the API shows its output on a page opened mid-action, running until it is done | t-out |
| choice | a stack's choices in the wizard: the rows, "Your own" holding Install until its fields are filled, an install on PostgreSQL that runs with the database it brought, the answer recorded; needs a catalog carrying x-fjord.choices (`CHOICE_CATALOG`, default `proof`) | t-choice |
| store-off | the store with every catalog switched off says so, with one button to Settings, Catalogs; the wrapper turns the catalogs off before and back on after | (none) |
| names-linked | a fjord linked to a proxy elsewhere, the Linux case: the wizard offers a name, refuses one another fjord holds, the app installs with its name, Open and the dashboard go to it, the name follows a port change, Remove name takes it away; the wrapper checks the name from off-host. Skipped unless linked (`APP`, default apprise) | t-nm |
| folders | a second copy of an app gets folders of its own; one inside another stack's is refused | |
| outcome | a failed install is said on the page and in the log; rows with no container say so | t-fail |
| arch | a tag with no build for this host is refused before and after | t-arch |
| retag-var | Change Version on an image whose tag is a variable sets .env and leaves the compose as written | t-var |
| leftover | a private network whose stack is gone reads as left over and can be deleted | |
| appjail-dns | the Setup check for jail name resolution tells the truth: with dnsmasq stopped it is a terminal job with the commands on screen, and gone once they are run (leaves dnsmasq configured for appjail) | |
| appjail-update | one service of an AppJail stack updated from the panel, only its jail rebuilt; then rolled back from its row: the jail runs the image it had, pinned, and the other jail never restarts | t-aju |
| privdel | deleting a stack removes its private network | t-pd |
| apply | Apply recreates what a Save changed, even with an exec session holding the old container | t-apply |
| wire | New Network asks the wire; networks that do not fit it are flagged | |
| type | Type per service (networks / host / none) on both engines; published ports kept | t-tc, t-tcj, t-lan |
| matrix | the network matrix: DHCP, pool, static, bridge, on podman and AppJail | t-* (KEEPs t-br for the port check) |
| multi | one app on two networks, via the wizard and the Services tab | t-m2, t-j2, t-ms, t-js, t-na, t-nb |
| extra | Setup, a multi-service stack on both engines, Adopt | t-adopt and paperless copies |
| restart | the restart chip on a stack's page: a new podman install restarts unless stopped, an AppJail install starts at boot and its chip offers only the boot choices, Don't start at boot saved and kept; each policy saved, applied, in the compose and shown after a reload; a crash left down under Don't restart (crash restarts under the others SKIP while the host check fdescfs fails: without fdescfs on /dev/fd conmon cannot free the port) | t-restart, t-restart-aj |
| lnms-admin | opt-in: LibreNMS's admin password is honoured as given (needs a librenms image from 2026-09-30 or later) | t-lnms |

### reboot (by hand)

Start on boot across a real reboot of the host, one stack per rule on both
engines. Not in the suite and not run by the runner: the host reboots
between the two steps, and that is yours to do.

    cd /root/fjord-ui-tests     # on the host, with reboot.js copied there
    podman run --rm --network host -e STEP=setup -v $PWD/reboot.js:/app/t.js:ro \
      -v $PWD/out:/out ghcr.io/daemonless/playwright:latest node /app/t.js
    shutdown -r now
    # back up, once fjordd has started its stacks:
    podman run --rm --network host -e STEP=check -v $PWD/reboot.js:/app/t.js:ro \
      -v $PWD/out:/out ghcr.io/daemonless/playwright:latest node /app/t.js

Then delete the `t-rb-*` stacks. On a host with `podman_enable=YES`,
podman's own rc script also starts the `always` stack at boot and can
collide with fjordd's start.

Not here: `wire-persist` (the warning kept across a restart) needs the
wrong-wire `lan-range` fixture, which netlab no longer has.

Known gap, printed as `SKIP` by `multi`: an AppJail jail given a second
network on the Services tab after install gets an address on one interface
only (0.3.5, AppJail parity). The wizard path gets both.
