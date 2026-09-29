// Turning an app's networking declaration into the interface list the install
// wizard shows.
//
// Lived inline in InstallWizard as a reactive block, where it could only be
// checked by installing something and looking -- and it silently produced a
// service with NO interfaces and no way to tell why.

export const PRIVATE = 'private';
/** Modes a service is put ON, as opposed to networks it joins. */
export const MODES = ['host', 'bridge', 'none'];

export type Iface = { network: string; ip?: string; ip6?: string; mac?: string };

/** A network as the daemon reports it. */
export type Net = { name: string; private?: boolean; addressSource?: string };

/**
 * joinable drops the networks that are some stack's own private segment.
 *
 * They are not somewhere to PUT an app: a stack's segment exists for its own
 * parts, and one is left behind by every multi-service install, so taking the
 * first network on the host made an orphaned `immich_priv` the default the
 * next app was offered. It is the same rule the picker folds them away by.
 */
export const joinable = (networks: Net[]) =>
  networks.filter((n) => !n.private && n.addressSource !== 'engine');

export const isMode = (spec: string) => MODES.includes(spec);

/**
 * The built-ins that stand alone. podman-compose refuses `networks` and
 * `network_mode` in one service, so host and none clear every interface.
 * bridge is not one of them: it is compose's `default` network, and a service
 * can be on it and on a LAN at once (tried on netlab: both answer).
 */
export const ALONE = ['host', 'none'];

/** What the Type pulldown says for a service: networks, host or none. */
export const serviceType = (rows: Iface[]) =>
  rows.length === 1 && ALONE.includes(rows[0].network) ? rows[0].network : 'networks';

/**
 * switchType moves a service between networks, host and none.
 *
 * Leaving networks keeps the rows aside (`kept`) and coming back restores
 * them: picking host used to throw every interface away, and only a page
 * refresh brought them back.
 */
export function switchType(
  rows: Iface[],
  to: string,
  kept: Iface[] | undefined,
  fallback: Iface[],
): { rows: Iface[]; kept?: Iface[] } {
  if (ALONE.includes(to)) {
    return { rows: [{ network: to }], kept: serviceType(rows) === 'networks' ? rows : kept };
  }
  if (serviceType(rows) === 'networks') return { rows, kept };
  return { rows: kept?.length ? kept : fallback };
}

/**
 * resolveDefault turns the manifest's "default" spec into a real network.
 *
 * "default" means "whatever this install is for", so it has to become
 * something concrete before the rows can show it. A MODE is not a network:
 * falling back to one (the configured default can be `bridge`) gave the
 * exposed service a built-in, which the seed then rendered as no interfaces
 * at all.
 *
 * A host with NO attachable network installs the app as it SHIPS, and the
 * whole stack together. Falling back to a built-in for the exposed service
 * alone splits the stack across places that cannot see each other -- a mode is
 * exclusive, so immich-server would sit on podman's bridge while its database
 * sat on a private segment -- and that installs cleanly and does not work.
 * Whichever way the app already arranges itself is the one arrangement known
 * to work, and its ports reach this host either way.
 *
 * `shipped` is that arrangement: "host" for a stack whose compose declares
 * network_mode: host (immich, which addresses its own parts over localhost),
 * "bridge" otherwise.
 */
export function resolveDefault(
  plan: Record<string, string>,
  networks: Net[],
  configured: string,
  shipped: string = 'bridge',
): Record<string, string> {
  if (!Object.values(plan).includes('default')) return plan;
  const real = joinable(networks).map((n) => n.name);
  const pick = real.includes(configured) ? configured : real[0] || '';
  if (!pick) {
    return Object.fromEntries(Object.keys(plan).map((k) => [k, shipped]));
  }
  return Object.fromEntries(
    Object.entries(plan).map(([k, v]) => [k, v === 'default' ? pick : v]),
  );
}

/**
 * seedInterfaces builds each service's interfaces from the resolved plan.
 *
 * A service on a real network also joins the private one -- it still has to
 * reach the rest of its own stack. Not when it IS the stack: a one-service app
 * has nothing to reach, and zensical came up with a pointless 10.100 address.
 * A service on a MODE gets exactly that one row: a mode is the whole answer,
 * and showing it as a row is what makes it visible and changeable. Never an
 * empty list, which reads as "broken".
 */
export function seedInterfaces(
  services: string[],
  plan: Record<string, string>,
): Record<string, Iface[]> {
  const out: Record<string, Iface[]> = {};
  for (const svc of services) {
    const spec = plan[svc] || PRIVATE;
    if (isMode(spec)) {
      out[svc] = [{ network: spec }];
    } else if (spec === PRIVATE) {
      out[svc] = [{ network: PRIVATE, ip: '', mac: '' }];
    } else if (services.length === 1) {
      out[svc] = [{ network: spec, ip: '', mac: '' }];
    } else {
      out[svc] = [
        { network: spec, ip: '', mac: '' },
        { network: PRIVATE, ip: '', mac: '' },
      ];
    }
  }
  return out;
}

/**
 * The attachments and modes an interface list means to the daemon.
 *
 * A service the operator left with NO usable interface means "none", stated
 * rather than omitted: a service that appears in neither list is never
 * touched, so one whose compose ships `network_mode: host` -- which is every
 * immich service -- would silently stay on the host's stack after being
 * emptied on screen.
 */
export function splitPlan(edits: Record<string, Iface[]>): {
  networks: { network: string; service: string; ip: string; ip6: string; mac: string }[];
  modes: Record<string, string>;
} {
  const networks: { network: string; service: string; ip: string; ip6: string; mac: string }[] = [];
  const modes: Record<string, string> = {};
  for (const [svc, rows] of Object.entries(edits)) {
    let placed = false;
    // bridge alone is the mode (no networks key); next to other networks it
    // is an interface on compose's default network.
    const alone = rows.filter((r) => r.network).length === 1;
    for (const r of rows) {
      if (!r.network) continue;
      placed = true;
      if (ALONE.includes(r.network) || (r.network === 'bridge' && alone)) {
        modes[svc] = r.network;
        continue;
      }
      networks.push({
        network: r.network,
        service: svc,
        ip: (r.ip ?? '').trim(),
        ip6: (r.ip6 ?? '').trim(),
        mac: (r.mac ?? '').trim(),
      });
    }
    if (!placed) modes[svc] = 'none';
  }
  return { networks, modes };
}

/**
 * setInterface applies one edit to a service's interface list.
 *
 * Choosing host or none replaces the whole list: a container on the host's
 * stack or on nothing holds no other interface. The caller is told what went,
 * so it can say so. bridge is an ordinary row.
 */
export function setInterface(
  rows: Iface[],
  i: number,
  field: 'network' | 'ip' | 'ip6' | 'mac',
  value: string,
): { rows: Iface[]; replaced: Iface[] } {
  if (field === 'network' && ALONE.includes(value)) {
    const replaced = rows.filter((_, j) => j !== i).filter((r) => r.network);
    return { rows: [{ network: value }], replaced };
  }
  const out = rows.map((r, j) => (j === i ? { ...r, [field]: value } : r));
  // A different network invalidates the addresses from the old one -- both
  // families, since each belonged to a segment this row has just left.
  if (field === 'network') out[i] = { ...out[i], ip: '', ip6: '' };
  return { rows: out, replaced: [] };
}

/**
 * Services that can no longer reach any other part of their own stack.
 *
 * Removing an interface is the one edit here that breaks a working stack
 * silently: immich's database is only ever reached over the private segment,
 * so taking it off leaves postgres running, healthy, and invisible to the
 * server that needs it. Nothing on the row says so -- the row just has one
 * fewer entry -- and the failure turns up later as the app refusing to start.
 *
 * Sharing is by name, and a MODE counts as one: two services on "host" share
 * this host's stack and reach each other over localhost, which is how the
 * bundles ship. "none" shares nothing with anyone, including another "none".
 *
 * A row naming no network is the project default -- a service with no
 * `networks:` key -- and every such service is on it together, so it counts
 * as "bridge". Hand-written and 0.2 composes are all like that.
 *
 * A single-service stack has nobody to be cut off from, so it never warns.
 */
export function isolatedServices(edits: Record<string, Iface[]>): string[] {
  const names = Object.keys(edits);
  if (names.length < 2) return [];
  const reachable = (svc: string) =>
    new Set((edits[svc] ?? []).map((r) => r.network || 'bridge').filter((n) => n !== 'none'));
  const out: string[] = [];
  for (const svc of names) {
    const mine = reachable(svc);
    const shares = names.some((other) => {
      if (other === svc) return false;
      for (const n of reachable(other)) if (mine.has(n)) return true;
      return false;
    });
    if (!shares) out.push(svc);
  }
  return out;
}

/**
 * keepAttachable carries the operator's interface edits over to a new
 * engine's network list instead of re-seeding them.
 *
 * Changing the engine re-seeded the wizard's table from the app's defaults,
 * so a network picked a moment earlier was replaced as soon as the engine
 * changed. What really has to go is only what the new engine cannot attach:
 * a row on a network it does not list, or a built-in it cannot do
 * (unsupported). A service left with nothing takes its fresh seed.
 */
export function keepAttachable(
  edits: Record<string, Iface[]>,
  fresh: Record<string, Iface[]>,
  networks: Net[],
  unsupported: string[] = [],
): Record<string, Iface[]> {
  const ok = (n: string) =>
    (isMode(n) && !unsupported.includes(n)) || n === PRIVATE || networks.some((x) => x.name === n);
  const out: Record<string, Iface[]> = {};
  for (const svc of Object.keys(fresh)) {
    const kept = (edits[svc] ?? []).filter((r) => ok(r.network));
    out[svc] = kept.length ? kept : fresh[svc];
  }
  return out;
}

// addressesNeeded lists "svc on net" for per-service rows that need an address
// typed in: a static network allocates nothing, and appjail cannot draw from a
// pool network's IPAM. Checking the stack-wide field instead left Install
// disabled by a field the per-service editor does not show.
export function addressesNeeded(
  edits: Record<string, Iface[]>,
  networks: { name: string; addressSource?: string }[],
  engine: string,
): string[] {
  const out: string[] = [];
  for (const [svc, rows] of Object.entries(edits)) {
    for (const r of rows) {
      const n = networks.find((x) => x.name === r.network);
      if (!n || (r.ip ?? '').trim()) continue;
      if (n.addressSource === 'static' || (engine === 'appjail' && n.addressSource === 'pool')) out.push(`${svc} on ${r.network}`);
    }
  }
  return out;
}
