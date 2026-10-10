# Changelog

Releases are tagged on GitHub; each rc is a pre-release with the pull
requests it carries. This file is the story per release.

## 0.3.1 — unreleased

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

### Install and your data
- A stack's data folder is named after the stack, so a second copy of an app
  never shares the first one's folder. Reinstalling under the same name finds
  the kept data and reuses its passwords instead of locking the app out of
  its database. (#94)
- A port another running stack publishes is refused at install, whatever
  the engine. (#95)
- On a network that gives an app its own address, the wizard shows the app's
  own port instead of a port field that would do nothing. (#99)
- A **public URL** field (the address an app is opened at, for the links in
  the mail it sends) is suggested from where the app lands and checked
  before install. (#103)
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
- A delete cut short by a fjordd restart is finished when fjordd starts,
  instead of the stack coming back up. (#105)

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
