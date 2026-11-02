# fjord

A web UI for running compose stacks on FreeBSD (and Linux) — app store,
stack lifecycle, storage, updates, and host diagnostics, in one static Go
binary with an embedded UI.

Born of the [daemonless.io](https://daemonless.io) effort to make native
FreeBSD OCI containers a first-class experience, but engine-neutral by
design: the same compose runs on **podman** or **AppJail**, and the catalog
format carries nothing vendor-specific.

> **fjord is root on its host.** Whoever signs in can run anything there.
> It speaks plain HTTP, so the password crosses your network unencrypted:
> keep it on a trusted LAN, or put a reverse proxy with HTTPS in front.
> Never forward port 3567 to the internet.

## What's new in 0.4

- **Linux.** fjord runs on Fedora and Debian (and Ubuntu) with rootful
  podman. Each release has a .deb and an .rpm that start fjord and say where
  to open it; LAN networks work there too, and Setup checks the firewall
  and SELinux.
- **Sign in.** fjord has an account. The first visit asks for a setup code
  printed on the host; `fjordd passwd` resets the password.
- **Names and HTTPS for your apps.** With the reverse proxy from the store,
  apps get names like `radarr.apps.lan` over HTTPS, given from the page.
  A second fjord joins the same proxy by pasting one link.
- **A Linux app store.** 183 apps, each installed and checked on Linux
  before it is offered.
- **AppJail:** roll back, pin or change one service's version.
- Apply recreates only what changed; a restart policy per stack; the
  sidebar is a drawer on phones. The full list is in
  [CHANGELOG.md](CHANGELOG.md).

## Install a release (FreeBSD host)

```sh
pkg install podman sysutils/podman-compose conmon ocijail
V=0.4.1   # or a pre-release: https://github.com/daemonless/fjord/releases
fetch -o /usr/local/sbin/fjordd https://github.com/daemonless/fjord/releases/download/v$V/fjordd-freebsd-$(uname -m)
fetch -o /usr/local/etc/rc.d/fjordd https://github.com/daemonless/fjord/releases/download/v$V/fjordd.rc
chmod 755 /usr/local/sbin/fjordd /usr/local/etc/rc.d/fjordd
sysrc fjordd_enable=YES podman_service_enable=YES
service podman_service start
service fjordd start
```

Then open `http://<host>:3567` and set up the account with the setup code
fjord made on the host ([Signing in](#signing-in)):
`cat /var/db/fjord/setup-code`.

LAN networks (a container with its own address) need the
[cni-epair](https://github.com/daemonless/cni-epair) plugin; the **System**
page says how to install it.

AppJail stacks need appjail 5.5 or newer, and containers finding each other
by name needs `cni-dnsname`. Until the 2026Q4 quarterly packages are built,
both come from the `latest` package set
(`/usr/local/etc/pkg/repos/FreeBSD.conf`: `url: "pkg+https://pkg.FreeBSD.org/${ABI}/latest"`).

## Install a release (Linux host)

fjord runs on Linux with rootful podman (Fedora, Debian, Ubuntu). Each
release has a package for both, which pulls in podman and podman-compose,
starts podman's API socket and fjord, and says where to open it:

```sh
V=0.4.1 A=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
# Fedora
dnf install -y https://github.com/daemonless/fjord/releases/download/v$V/fjord-$V-linux-$A.rpm
# Debian/Ubuntu
curl -fsSLO https://github.com/daemonless/fjord/releases/download/v$V/fjord-$V-linux-$A.deb && apt install -y ./fjord-$V-linux-$A.deb
```

Updating is the same command with the new version: fjord restarts and the
apps keep running. Removing the package keeps `/var/db/fjord` (your apps
and their data). A package installed over the installer's copy below moves
that copy aside (`*.install-linux`).

Or the installer, which checks podman, installs `fjordd` and its systemd
unit in /usr/local, and starts podman's API socket and fjord:

```sh
# Fedora: dnf install -y podman podman-compose   Debian/Ubuntu: apt install -y podman podman-compose
curl -fsSL https://github.com/daemonless/fjord/releases/latest/download/install-linux.sh | sudo sh
```

Or by hand:

```sh
V=0.4.1
curl -fsSLo /usr/local/sbin/fjordd https://github.com/daemonless/fjord/releases/download/v$V/fjordd-linux-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
curl -fsSLo /etc/systemd/system/fjordd.service https://github.com/daemonless/fjord/releases/download/v$V/fjordd.service
chmod 755 /usr/local/sbin/fjordd
systemctl daemon-reload
systemctl enable --now podman.socket fjordd
```

On Linux:
- Set up the account with the code fjord made on the host:
  `sudo cat /var/db/fjord/setup-code` ([Signing in](#signing-in)). Keep
  port 3567 on your LAN; never forward it to the internet.
- A host firewall (firewalld on Fedora Server, ufw) blocks the page from the
  LAN until port 3567 is let in; the **System** page says how. Apps'
  published ports need nothing: podman opens those.
- With SELinux enforcing, fjord labels the folders it makes for an app (App
  data) for containers. A folder of your own (a media library) needs `:z` on
  the mount, or `chcon -R -t container_file_t <dir>`; fjord says which.
- LAN networks (a container with its own address) are macvlan on a host
  interface. DHCP on them needs netavark's proxy:
  `systemctl enable --now netavark-dhcp-proxy.socket`. The DHCP server sees
  each container by its own id, so a recreated app keeps its address only
  through a reservation for the MAC fjord pins -- or use a range network,
  where fjord pins the address itself.
- The App Store shows apps for Linux (the linuxserver.io catalog); FreeBSD
  images are left out.

## Build from source (FreeBSD host)

```sh
# toolchain the podman engine needs
pkg install podman sysutils/podman-compose conmon ocijail
# optional second engine (appjail + its director, which fjord drives)
pkg install appjail sysutils/py-director

# build (needs go + npm)
cd ui && npm ci && npm run build && cd ..
go build -o fjordd ./cmd/fjordd

# install as a service
cp fjordd /usr/local/sbin/
cp packaging/fjordd.rc /usr/local/etc/rc.d/fjordd
chmod 755 /usr/local/etc/rc.d/fjordd
sysrc fjordd_enable=YES podman_service_enable=YES
service podman_service start
service fjordd start
```

Open `http://<host>:3567` and set up the account with the code in
`/var/db/fjord/setup-code`. The **System** page runs host-readiness checks
(missing tools, dead podman socket, pf rules) with copyable fixes — start
there if anything misbehaves.

## Key Features

- **Native Jail Containment** — Zero Linux VM overhead. Workloads run directly on the FreeBSD kernel with native ZFS dataset performance, resource limits, and network isolation.
- **Dual Engine Backends** — Run Podman (with `ocijail`) and native FreeBSD jails managed by `appjail-director` side by side on the same host, selectable per stack at deploy time.
- **Filesystem-First Ground Truth** — No opaque databases. Every stack is a plain directory containing standard `compose.yaml` and `.env`. The web UI and CLI tools (`podman-compose`, `appjail-director`) stay synchronized without private lock files.
- **Adopt What's Already Running** — Containers and jails started by hand or by a playbook become stacks in one click: fjord turns a podman container's original run command into `compose.yaml`, or reads an appjail jail back into a director bundle, and replaces it in place, keeping the image, mounts, network address and name. The setup wizard offers it on first run; "Adopt & replace all" converts a whole host.
- **Automated Host Diagnostics** — Built-in pre-flight checks inspect the Libpod API socket, Packet Filter redirection anchors (`cni-rdr` / `appjail-nat`), container init tools, and port conflicts before deployment.
- **Structured Storage & Folder Sets** — Clean architectural separation between internal application state and persistent user data pools (local ZFS datasets, NFS exports, or SMB shares) mapped with reusable Folder Sets.
- **Single Self-Contained Binary** — Written in Go with an embedded Svelte SPA frontend. Zero background Python runtimes, Node daemons, or database dependencies.

## Signing in

fjord has one account: a user name and a password.

- **First run.** A fresh fjord has no account. It writes a setup code to
  `/var/db/fjord/setup-code` (readable by root only) and the page asks for
  it, so only someone with root on the host can claim fjord. Or create the
  account before the first start, and there is no setup page:
  `fjordd passwd` (or `echo "$PW" | fjordd passwd --password-stdin` from a
  playbook).
- **Forgot the password?** On the host, as root: `fjordd passwd`. It also
  signs every browser out. If fjordd keeps its stacks somewhere other than
  `/var/db/fjord/stacks`, add `--stacks-dir <that dir>`.
- **Sessions** last 30 days from last use and survive restarts and upgrades.
  **Settings → Account** lists where you are signed in, signs a browser out,
  and changes the user name and password.
- **No HTTPS yet.** fjord speaks plain HTTP. For HTTPS, put a reverse proxy
  in front; tell fjord its address with `FJORD_TRUSTED_PROXY` so sign-ins
  are recorded from the real address and cookies are marked Secure.
- **A login of your own in front** (authelia, authentik, a Tailscale-only
  proxy): `FJORD_AUTH=none` turns fjord's sign-in off, but only when
  nothing else can reach it -- fjordd listens on loopback only, or
  `FJORD_TRUSTED_PROXY` names the proxy and everything else is refused.

## Configuration

Environment variables (via `fjordd_env` in rc.conf):

| Variable | Default | Purpose |
|---|---|---|
| `FJORD_LISTEN` | `:3567` | HTTP listen address |
| `FJORD_STACKS_DIR` | `/var/db/fjord/stacks` | stack storage (parent dir = fjord root) |
| `FJORD_STORAGE_BASE` | `<fjord root>/containers` | default App data location until one is set in Settings |
| `FJORD_CATALOG_URL` | *(unset)* | bootstraps a first app-store catalog on a fresh install; catalogs are managed in Settings afterwards |
| `FJORD_ENGINE` | `podman` | default engine for new installs until one is set in Settings |
| `FJORD_PODMAN_SOCKET` | `/var/run/podman/podman.sock` | libpod API socket |
| `FJORD_HOST_ADDR` | `127.0.0.1` | address probed for host port conflicts |
| `FJORD_ALLOWED_HOSTS` | *(unset)* | more names fjord answers to, comma-separated (a reverse proxy's) |
| `FJORD_TRUSTED_PROXY` | *(unset)* | reverse proxy addresses or ranges, comma-separated: their `X-Forwarded-For` / `X-Forwarded-Proto` are believed |
| `FJORD_AUTH` | `password` | `none` turns sign-in off; refused unless `FJORD_LISTEN` is loopback or `FJORD_TRUSTED_PROXY` is set |

Everything else — App data locations, folder sets, catalogs, engines,
plugins — lives in Settings and is persisted in `<fjord root>/settings.json`.

## Notes and limitations

- Bridge-network port publishing on FreeBSD needs pf with podman's rdr
  anchors (`/usr/local/etc/containers/pf.conf.sample`); the setup wizard
  and the System page add them to `/etc/pf.conf` for you.
- Stacks are plain `compose.yaml` + `.env` on disk — fjord's state lives
  beside them in `state.json`, and nothing stops you editing by hand.
- Host-network stacks can declare their web endpoint for the ↗ Open link:
  ```yaml
  x-fjord:
    web_port: "8080"
  ```
- An app that gives the other apps names (`radarr.apps.lan`), such as the
  daemonless reverse proxy, declares `x-fjord.provides.names` in its
  catalog manifest; fjord pairs with it at install. The contract any such
  app serves is in [docs/names-provider.md](docs/names-provider.md).
- FreeBSD's built-in SMB client speaks SMB1 only, which most servers refuse:
  `smb://` folders and volumes work from Linux hosts; use NFS on FreeBSD.
- On AppJail a jail can sit on a LAN network with its own address (DHCP or
  static) or on a private one. DHCP needs the image to ship
  `/etc/rc.d/dhclient`; without it, give the jail a static address. Named
  volumes, and so remote folders, still need the podman engine.
- Editing a folder set does not touch stacks already installed from it.

## Contributing

Go code is `gofmt`-clean and `go vet`/`go test ./...` pass; the UI is
Svelte 5 (legacy syntax) built with Vite — `npm run check` for types.
`scripts/e2e.sh <host>` runs the end-to-end suite against a live fjordd on a
disposable host and checks it left nothing behind. See
`DESIGN.md` for the architecture and the releases page for what shipped.
