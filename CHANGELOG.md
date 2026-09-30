# Changelog

Releases are tagged on GitHub; each rc is a pre-release with the pull
requests it carries. This file is the story per release.

## 0.3.0 — unreleased (rc4 is main at #57, 2026-09-30)

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

### Fixes
- The App Store keeps the network picked in the wizard (#37); wizard,
  Services tab and Networks page fixes (#41); rc3 review fixes for adopt,
  pf and setup (#38); SBOM reads cosign 3 bundle attestations (#43); the
  dev proxy no longer rewrites Host (#1).

### Testing
- `scripts/e2e.sh <host>` runs the Go end-to-end suite against a live fjordd
  and checks for leftovers (#19, #27, #29). `scripts/ui-tests.sh <host>`
  runs the browser tests through the real pages (#55).

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
