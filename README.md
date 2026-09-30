# fjord

A web UI for running compose stacks on FreeBSD (and Linux) — app store,
stack lifecycle, storage, updates, and host diagnostics, in one static Go
binary with an embedded UI.

Born of the [daemonless.io](https://daemonless.io) effort to make native
FreeBSD OCI containers a first-class experience, but engine-neutral by
design: the same compose runs on **podman** or **AppJail**, and the catalog
format carries nothing vendor-specific.

> **Tech preview — no authentication.** Anyone who can reach the port can
> run containers as root on the host. Keep it on a trusted LAN, or bind it
> to loopback (`FJORD_LISTEN=127.0.0.1:3567`) and reach it over SSH.

## What's new in 0.3

- **Updates you can see into and undo.** The Update panel lists what each
  service would get, in plain words ("New build of the same version —
  3 packages changed"), and lets you tick which ones to update, version bumps
  included. After an update fjord watches the new container and marks the
  update failed if it crash-loops; **Roll back** returns to the previous image.
  A version with no build for this host is refused before it is pulled.
- **Networks that work.** A service gets an address of its own on your LAN,
  by DHCP, from a pool or typed in; it keeps that address through updates,
  restarts and reboots, and an address another stack has is refused. Each
  service picks its **Type**: networks, host or none. **New Network** asks the
  wire what subnet it is and refuses one that is not on it.
- **Setup gets a host ready.** One screen, one thing at a time: an Install
  button for what fjord can do itself, the exact commands for what it leaves
  to you. **Adopt** takes over containers and jails you started by hand.
- **Nothing lies.** Apply confirms the containers were really recreated; a
  failed install or update stays on the stack page with its reason; a busy
  stack says so. The lesson of this release: judge by the outcome, not the
  exit code.
- **Delete says what it removes.** App data is kept unless you tick it, and
  **System → Left-over app data** finds folders earlier deletes left behind.
- The compose tab explains its `${VAR}`s; stack list filters; a one-line
  health summary; upgrades of fjord itself can be undone
  (`/var/db/fjord/backups/`).

Not in 0.3, said plainly: no authentication (0.4); AppJail stacks update as a
whole, without rollback (0.3.5); private networks are not isolated from other
stacks on the host (0.3.6); no Linux hosts. The full list is in
[CHANGELOG.md](CHANGELOG.md).

## Install a release (FreeBSD host)

```sh
pkg install podman sysutils/podman-compose conmon ocijail
V=0.3.0   # or a pre-release: https://github.com/daemonless/fjord/releases
fetch -o /usr/local/sbin/fjordd https://github.com/daemonless/fjord/releases/download/v$V/fjordd-freebsd-$(uname -m)
fetch -o /usr/local/etc/rc.d/fjordd https://github.com/daemonless/fjord/releases/download/v$V/fjordd.rc
chmod 755 /usr/local/sbin/fjordd /usr/local/etc/rc.d/fjordd
sysrc fjordd_enable=YES podman_service_enable=YES
service podman_service start
service fjordd start
```

LAN networks (a container with its own address) need the
[cni-epair](https://github.com/daemonless/cni-epair) plugin; the **System**
page says how to install it.

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

Open `http://<host>:3567`. The **System** page runs host-readiness checks
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

Everything else — App data locations, folder sets, catalogs, engines,
plugins — lives in Settings and is persisted in `<fjord root>/settings.json`.

## Notes and limitations

- Bridge-network port publishing on FreeBSD needs pf with podman's rdr
  anchors (`/usr/local/etc/containers/pf.conf.sample`); the System page
  checks for this.
- Stacks are plain `compose.yaml` + `.env` on disk — fjord's state lives
  beside them in `state.json`, and nothing stops you editing by hand.
- Host-network stacks can declare their web endpoint for the ↗ Open link:
  ```yaml
  x-fjord:
    web_port: "8080"
  ```
- FreeBSD's built-in SMB client speaks SMB1 only, which most servers refuse:
  `smb://` folders and volumes work from Linux hosts; use NFS on FreeBSD.
- AppJail has no named volumes or networks yet, so remote folders and
  macvlan IPs need the podman engine.
- Editing a folder set does not touch stacks already installed from it.

## Contributing

Go code is `gofmt`-clean and `go vet`/`go test ./...` pass; the UI is
Svelte 5 (legacy syntax) built with Vite — `npm run check` for types.
`scripts/e2e.sh <host>` runs the end-to-end suite against a live fjordd on a
disposable host and checks it left nothing behind. See
`DESIGN.md` for the architecture and the releases page for what shipped.
