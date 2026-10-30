# Changelog

Releases are tagged on GitHub; each rc is a pre-release with the pull
requests it carries. This file is the story per release.

## 0.4.0 — unreleased

Linux, a sign-in, names and HTTPS for your apps, and a Linux app store.
Proven through the real pages on a FreeBSD host and a Fedora host
(`test/ui`), and by hand on a Debian one.

### Linux
- fjord runs on Linux: Fedora and Debian with rootful podman. Each release
  carries `fjordd-linux-amd64` and `-arm64`, a systemd unit and
  `install-linux.sh`, which checks podman, enables its socket and says how
  to open the firewall. (#149)
- A .deb and an .rpm per arch (`fjord-<version>-linux-<arch>`): installing
  starts fjord and says where to open it, updating restarts it with the
  apps left running, removing keeps `/var/db/fjord`. Installed over
  `install-linux.sh`'s copy, it moves that copy aside.
- LAN networks on Linux: macvlan with DHCP through netavark's proxy or a
  static pool, the wire probe, and the Open link on the container's own
  address. The host firewall and SELinux are checked at Setup; a folder
  SELinux refuses gets `:z`, and mount options can be set from the page.
  (#149, #155)
- Networks and addresses are read through podman, engines another OS does
  not run are left out, and the store offers only what this OS can run.
  (#149)
- Import a compose project started outside fjord. (#149)

### Sign in
- fjord has an account. The first visit asks for a setup code printed on
  the host; after that every page and the API need a session, which ends
  after 30 days unused. `fjordd passwd` resets it; `FJORD_AUTH=none` keeps
  the old behaviour behind a reverse proxy of your own.
- The password a person signs in to an app with is one they can type, and
  the app's own login shows beside Open ("Sign in"), made up by fjord at
  install or stated as the app's default. (#155)

### Names and HTTPS
- An app that declares it provides names (the reverse proxy from the
  store) gives the others names like `radarr.apps.lan` over HTTPS. Names
  are given, changed and removed from the ▾ beside Open and at install;
  a shield says whether this device trusts the proxy or can resolve the
  names; names follow an app's port and address. Every fjord has an id and
  a name, and a second fjord links to the same proxy by pasting one link.
  The contract is `docs/names-provider.md`.
- A provider that takes icons (the reverse proxy) is sent each app's icon
  with its name, so its list of names shows the same logos as fjord. Names
  made before get theirs when fjordd starts.

### A Linux app store
- 183 linuxserver.io apps, each one installed and checked on a Linux host
  before it is offered; a fresh Linux install starts with that catalog,
  FreeBSD with daemonless's. Optional settings are offered under Advanced
  and left out when empty; an app whose default port is taken starts on a
  free one; a value typed in .env reaches the app in one click. (#149,
  #155)

### AppJail
- Roll back, pin and change one service's version; updated jails are
  watched to stay up as containers are; jail name resolution is set up in
  one click without an open resolver; a stale director lock no longer
  wedges a stack. (#139, #140, #149)

### Fixes and small things
- The names provider's row says Finish setup only before its setup is
  done. One set up before that fjord cannot reach (a certificate, a
  timeout) says so in words, and the page keeps checking after fjord
  itself did not answer instead of staying stuck until a reload.
- Apply recreates only what changed, and a second Apply during the first
  one's check takes over. A save during an install or an update is kept.
  Two installs at once get different names. (#142, #146, #147, #148, #149)
- A restart policy per stack, chosen on the page; a stack it leaves down
  at boot reads as stopped. (#134)
- The doctor checks that crash restarts can work (fdescfs) and that
  podman's API service is not older than podman; the Shell tab explains a
  missing shell or a stale service and offers the restart. (#136, #150)
- Install says what it downloads before Install; optional fields are
  marked. (#137, #138)
- Folders are never a system folder, never follow a planted symlink, and
  an app's secrets are kept where its container cannot reach. (#149)
- The sidebar is a drawer on phones; the Shell tab is readable in light
  mode; a long status line no longer runs under the buttons. (#133, #149)
<<<<<<< HEAD
<<<<<<< HEAD
=======
- A proxy linked by hand stays this fjord's names provider when the store's
  reverse proxy is also installed here; the installed one's key may be one
  the proxy dropped, which turned every name back into an address.
>>>>>>> f9c52a7 (fix(names): a proxy linked by hand stays the provider; the Names tab shows a refused key)
- A page load says Loading until the stack list arrives, instead of "No
  stacks yet" on a host that has stacks; a list that fails to load says
  so, with Try Again.
=======
- The stack list no longer looks over the host's network interfaces once
  per stack: at 18 stacks on FreeBSD it answers in 0.2 s instead of 2.3 s,
  and the first stack shows in about half the time.
>>>>>>> abde07b (fix: the stack list stops scanning the host's network interfaces per stack)

## 0.3.2 — 2026-10-21

Fixes only, for 0.3.1. New work goes into 0.4.0 (Linux).

- Apply after a save recreates only the services you changed. It recreated
  every container in the stack: podman-compose tears all of them down when
  any one's configuration differs from the file's. A second Apply during the
  first one's stay-up check now takes over instead of being refused. (#149)
- Two installs started at the same time no longer get the same stack name,
  the second writing over the first. (#146)
- A save made while an install is still checking its app stays up is kept,
  not put back to the install's copy. (#142)
- Adopting a jail with no network no longer gives it network "NETWORKS" at
  address "NETWORK_IP4". (#143)
- The Shell tab is readable in light mode. (#133)
- The Shell tab says to restart podman's API service when a podman upgrade
  left it unable to start shells, and stops retrying instead of looping.
  (#150)

## 0.3.1 — 2026-10-14

Stack choices, AppJail catching up, and fjord saying what is wrong instead
of making you find it. Proven on a test host through the real pages and the
install API, on both engines (`test/ui`, `test/e2e`).

### Stack choices
- An app can ask a question at install: which **database** (SQLite,
  PostgreSQL, MariaDB or your own), or which **parts** (Immich's machine
  learning, its public sharing proxy). The default answer is selected; the
  wizard's networks and options follow the pick, and the database's
  password is made up for you. (#69, #72, #79)
- Choices work on **AppJail** too: each option carries its AppJail form, the
  jails it adds find each other by name, and an install the bundle cannot
  run is refused before anything is saved. (#84, #92, #93)

### AppJail
- Host networking is a choice like any other. (#71)
- Update one service without restarting the whole stack. (#73)
- Jails on one network resolve each other by name (appjail-dns and dnsmasq;
  Setup checks both). (#93)
- Folders mount as podman's do: no copy-out that failed on an empty image
  folder, and each service gets its own folder. (#93)
- The version you pick reaches the jail when a choice adds a database, and
  Change Version works on such a stack. (#96)
- A jail with an address on your LAN gets its Open link. (#97)
- An install says how many jails it will build (AppJail builds them one at
  a time) and counts them off as it goes. (#109)
- An app whose AppJail bundle has no jail for one of its services (librenms's
  database) is held in the wizard with the reason, not refused after Install.
- A stack put on your LAN finds its own parts: immich's server reaches its
  database, cache and machine learning by jail name. A jail that takes a
  DHCP lease keeps the jail resolver, and an app's database stays private
  when the install names one network for the whole stack.
- A stack of several jails shows its live state instead of "appjail is
  busy": its jails are checked at once, and a jail on two networks gets its
  LAN address and Open link.
- The Variables panel reads a director file's `${VAR:default}`: a variable
  with a default, or one set in .env, is no longer listed as missing.
- Delete with its data removes a single AppJail app's folder. Its folders are
  read from the director file, which is what runs, so another stack's delete
  and the leftover-data check also see them in use.
- A stack's parts find each other when a hostname is named inside another
  value: grimmory's `DATABASE_URL` names `MARIADB_HOSTNAME`, which was left
  out of the jail's .env, so grimmory looked for its database in its own jail.
- A jail on its own LAN address is checked on its ports, not its log: authelia
  read "crashed" for minutes while serving, from a line logged before its
  cache was up.

### Install and your data
- A stack's data folder is named after the stack, so a second copy of an app
  never shares the first one's folder. Reinstalling under the same name finds
  the kept data and reuses its passwords instead of locking the app out of
  its database. (#94)
- A port another running stack publishes is refused at install, whatever
  the engine. (#95)
- A taken port or address is refused before anything is saved, with the
  whole reason in the wizard. It used to leave the stack saved and stopped,
  and the wizard showed only "pre-flight failed".
- On a network that gives an app its own address, the wizard shows the app's
  own port instead of a port field that would do nothing. (#99)
- A **public URL** field (the address an app is opened at, for the links in
  the mail it sends) is suggested from where the app lands and checked
  before install. (#103)
- A token only you have (cloudflared's tunnel token) starts empty and up
  front, and Install waits for it. The catalog's placeholder was filled in
  under Advanced, installed, and the app crash-looped.
- A host-network stack finds its parts at 127.0.0.1. (#78)
- A part that is on or off (Immich's machine learning, its sharing proxy) is
  one switch. With bridge, host or none as the default network, the app
  installs as it ships instead of on the first LAN. (#107)
- Immich with its sharing proxy switched off installs; the network plan
  named the part that was off. (#110)

### fjord says what is wrong
- A failed start says why, not "exit status 125"; podman's errors are in
  plain words. (#82, #83, #85)
- A crash loop shows the app's own error line ("service.publicurl must
  include http:// or https://"), in the output, on the service and in the
  failure banner. (#102)
- The status beside a failed action carries the reason instead of "HTTP 409"
  or "see Output"; the stack page says services, not containers. (#101)
- No false "crashed" while an app waits for its database on first boot, and
  a crash loop at install raises the failure banner. (#86)
- A pull that cannot fit is refused before it starts, with the space it
  needs. (#88)
- Start on boot says what came up; a busy stack says what it is doing and
  for how long. (#87)
- Changing a password the stack was set up with warns that the database
  still has the old one. (#91)
- A status not known yet is a grey "checking", not a red failure. (#80)
- Setup no longer calls a podman API socket "ok" after podman was removed. (#90)
- Setup sets the host's timezone (the one your browser is in) with the rest,
  and its by-hand fix pastes cleanly: it used to start `tzsetup`'s menu,
  which swallowed the rest of the pasted block.

### Faster pages
- Pages never wait on a busy engine: they show the last known state, marked,
  while a pull holds podman. (#77)
- A container's last log lines come from podman's API, not a command per
  container. (#98)

### Networks
- A stack's private network is not offered as the default for new installs.
  (#89)
- A jail on two networks shows its address on each, so the stack page has
  the right link to open. (#108)
- fjord's network probe no longer takes the host's name in your router's DNS
  (it asked DHCP under the host's name; the matching cni-epair fix is
  v1.1.3). (#100)
- An up stuck waiting on a service that failed is stopped, with the
  service's error. (#76)
- Setup warns when `ifconfig_DEFAULT` asks for DHCP: it runs dhclient on
  every interface that appears, container interfaces included. The fix names
  your real interface instead.

### Security
- fjordd answers only to this host's own names (DNS rebinding). (#74)
- One lock per stack around its state, so two actions cannot interleave.
  (#75)

### Also
- Editors, Output and Shell follow light mode (#70); Dismiss on a failure
  banner lasts past a reload (#68); a slow inspect during the health watch
  is not a container that is gone (#67); docs for 0.3.0 and AppJail LAN
  networks (#65, #66).
- A nightly pre-release of main, published only when its tests pass. (#81)
- A stack of several apps has a menu beside Open listing each one; the
  dashboard says how many more. (#106)
- The Update panel compares like with like: an app's own new version is one
  change, not one package removed and another added. (#111)
- A stack saved without an engine gets the default one.
- A delete cut short by a fjordd restart is finished when fjordd starts,
  instead of the stack coming back up. (#105)
- The check that an updated or installed app stays up no longer holds the
  stack: the actions come back as it starts, and using one ends the check,
  like Ctrl-C. An app that crashes on its own still fails the update.
- Updating a stack you stopped leaves it stopped: the new images are pulled
  and Start runs them. It used to start the stack (tailscale came up on its
  own). One that crashed or failed to install still comes up.
- A required secret left empty is made up at install (64 hex characters) and
  named in the output; it was the catalog's placeholder ("changeme",
  "your-encryption-key-here"). Secrets only you have (a tunnel token) say
  `generate: false` and are asked for as before.
- Why Install is greyed out is written beside it, not only in its tooltip. A
  required address that can't be filled in yet (the app's own address on a
  DHCP network is known once it starts) says to type it, or to use bridge.
- The install wizard says "checking engines…" and holds Install until it
  knows the host's engines; it said "podman" meanwhile, and an engine picked
  early was overwritten, so a stack could land on the wrong one.
- The version or channel picked at install moves only the app's own image. A
  database a choice added lost its pin: postgres:17 came up as
  postgres:latest, a later major than its data.
- A stack's services show their own app's icon, in the Services list and the
  Open menu (crafting-apps: ten blank squares). The Logs tab's Services menu
  has All and None, and "only" on each service.
- An AppJail stack left on the host's network no longer fails `up` with
  "expose requires the following options: virtualnet": a jail with no
  virtualnet drops its expose lines (woodpecker, paperless-ngx, redis).
- The App Store search finds an app by its id too, dashes and spaces alike:
  "uptime-kuma" found nothing (the name is "Uptime Kuma").
- A stack saved through the API without naming an engine runs on the
  default one; every start failed with "no container engine is available
  on this host". The page always named one.

### Known limitations in 0.3.1
- **No authentication.** Anyone who can reach port 3567 can run containers
  as root. Keep it on a trusted LAN. Authentication is 0.4.1; Linux comes
  first, in 0.4.0.
- AppJail: no rollback or health watch after an update yet; the built-in
  bridge cannot sit next to a LAN network on one jail. 0.3.5.
- A private network is not isolated from other stacks on the same host, only
  unreachable from your LAN. 0.3.6.
- **Packages.** AppJail stacks need appjail 5.5 or newer and container name
  resolution needs cni-dnsname; until the 2026Q4 quarterly packages are
  built, both come from the `latest` package set.

## 0.3.0 — 2026-10-02

Networks that work, updates you can trust. Everything here was proven on a
test host through the real pages: the containers, the addresses, what
answers (`test/ui`, `test/e2e`).

### Updates you can see into and undo
- The **Update** panel says what each service would get, in plain words
  (a new build of the same version, a patch, a minor or a major), and lets
  you tick which ones to update. Version bumps are offered per release train,
  and you can change to any published version. (#14, #15, #19, #26)
- After an update fjord watches the new container for 30 seconds and marks
  the update failed if it crash-loops; **Roll back** returns to the previous
  image. (#14)
- An update recreates the container so the new image is really the one
  running; the containers are asked, not the exit code. (#4, #49)
- **Apply** after a Save recreates exactly the services the Save changed and
  confirms it, even when an open exec session refuses the teardown. (#49)
- A version with no build for this host (an amd64-only tag on arm64) is said
  in the wizard and in Change Version, refused by the API, and the engine
  judges the pulled image before anything is torn down. (#52)
- AppJail stacks: update detection for jails, director projects included.
  Whole-stack updates only in 0.3 (see limits). (#16)
- fjordd saves its own state before a new version of itself starts,
  under `/var/db/fjord/backups/`. (#20)

### Networks that work
- LAN networks with the cni-epair plugin: a service gets an address of its
  own on your network, by DHCP, from a pool, or typed in. (#7, #9)
- Addresses stay put: the address (pool) or MAC (DHCP) is pinned the first
  time a service starts; an address another stack has is refused on Save and
  at start. (#19, #24, #28)
- **Type** per service: networks, host or none. The built-in bridge can sit
  next to a LAN network and a service on the bridge keeps its published
  ports. (#40, #44)
- **New Network** asks the wire: a caged DHCP probe fills in the subnet and
  gateway of a bridge the host has no address on, and a subnet that is not on
  that wire is refused. Networks that disagree with their wire are flagged,
  and the answers are kept across restarts. (#45, #48)
- **Open** goes to the address on your LAN, never a stack's private one; a
  one-service app gets no private network. (#46, #47)
- A stack's private network goes with the stack; one left over from before
  reads as left over and can be deleted. (#45, #51)
- IPv6 segments on pool and static networks. (#12)
- When a DHCP network never answers, the start says so in plain words. (#36)

### Setup and the host
- **Setup**: one screen that gets a fresh host ready, one thing at a time,
  with an **Install** button for what fjord can fix itself (cni-epair, pf
  anchors, services) and a "Your turn" card with the commands for what it
  leaves to you. (#31, #33)
- Host checks: NAT for bridge containers out of every network the host is
  on, safe to run again (#34); ocijail by patch level (#35); a podman service
  older than its packages (#11); a timezone for AppJail (#39); git and rage
  when a stack needs them (#17); jail name resolution needs dnsmasq answering
  with appjail's config, not just appjail-dns running (#56).
- **Adopt** reads `podman create` lines and AppJail arguments, and reports a
  stack that did not start. (#32)

### Stacks
- Busy while installing or updating: not a problem, a second action is
  refused, the greyed buttons say why. (#47, #50)
- A failed action is a banner on the stack page with the reason, and a line
  in fjordd's log, until the next action succeeds. A service with no
  container says "no container" instead of a yellow dot. (#53)
- **Delete** says what it removes; app data is kept unless you tick it, and
  **System → Left-over app data** finds folders earlier deletes left. (#21)
- The compose tab shows each `${VAR}` with its value from `.env`, and a
  Variables panel edits them. (#22)
- A second copy of an app gets folders of its own, and a folder inside
  another stack's directory is refused. (#54)
- Stack list filter (Running · Stopped · Problems · Updates) and a one-line
  health summary (#25); group by network (#3); dark and light theme (#2);
  Audiobooks and Ebooks folder sets (#23).
- The keyboard reaches what was mouse-only: store cards, the stack name,
  the resize sashes. (#57)
- The Output pane shows an action this page did not start (another tab, the
  API, start on boot, a reload), and follows it until it is done. (#62)
- A service whose app keeps crashing inside a container that stays up reads
  as crashed, with the reason, not running; an update that leaves it so is a
  failed update. (#64)
- A service alone on the built-in bridge shows eth0 once it runs, not
  "on create". (#60)

### Fixes
- The App Store keeps the network picked in the wizard (#37); wizard,
  Services tab and Networks page fixes (#41); rc3 review fixes for adopt,
  pf and setup (#38); SBOM reads cosign 3 bundle attestations (#43); the
  dev proxy no longer rewrites Host (#1). From rc4: the SBOM diff reads
  bundles as ghcr serves them, AppJail installs on the default network again,
  and Change Version works on an image whose tag is a variable. (#59)

### Testing
- `scripts/e2e.sh <host>` runs the Go end-to-end suite against a live fjordd
  and checks for leftovers (#19, #27, #29). `scripts/ui-tests.sh <host>`
  runs the browser tests through the real pages (#55), now with the day-one
  install on each engine's default network (#61) and an update through the
  panel end to end (#63).

### Known limitations in 0.3
- **No authentication.** Anyone who can reach port 3567 can run containers
  as root. Keep it on a trusted LAN or bind it to loopback and use SSH.
  Authentication is 0.4.
- AppJail stacks update as a whole: no per-service update, rollback, version
  change or health watch yet; the built-in bridge cannot sit next to a LAN
  network on one jail; a network added on the Services tab after install
  gets no second address. All 0.3.5.
- A private network is not isolated from other stacks on the same host, only
  unreachable from your LAN. 0.3.6.
- IPv6: no SLAAC on DHCP networks; v6 segments on pool and static networks
  only. 0.3.6.
- Linux hosts are not supported.
- **Packages.** AppJail stacks need appjail 5.5 or newer and container name
  resolution needs cni-dnsname. Until the 2026Q4 quarterly packages are
  built, both come from the `latest` package set; on quarterly, fjord runs
  podman stacks and Setup says what is missing for the rest.
