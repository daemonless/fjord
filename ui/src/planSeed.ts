// Turning an app's networking declaration into the interface list the install
// wizard shows.
//
// Lived inline in InstallWizard as a reactive block, where it could only be
// checked by installing something and looking -- and it silently produced a
// service with NO interfaces and no way to tell why.

export const PRIVATE = 'private';
/** Modes a service is put ON, as opposed to networks it joins. */
export const MODES = ['host', 'bridge', 'none'];

export type Iface = { network: string; ip?: string; mac?: string };

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
 * The built-ins ONE SERVICE can be put on.
 *
 * "bridge" is not one of them. host and none must clear the other interfaces:
 * podman-compose refuses `networks` and `network_mode` in the same service
 * ("networks and network_mode must not be present in the same service"), so
 * the format decides that, not fjord. bridge writes no network_mode at all --
 * it only means "hold no interface and take the project default" -- and fjord
 * grouped it with the other two, so it inherited an exclusivity nothing
 * imposes. On a multi-service stack that made it a trap: putting the exposed
 * service on bridge dropped the private interface it reaches its own database
 * through. "private" is the same NAT segment with the rest of the stack still
 * on it, and the stack-level mode picker still offers bridge, where it means
 * every service and is true.
 *
 * A stack with ONE service is the exception: there is nothing to strand, and
 * "publish my ports, join nothing" is the ordinary thing to want -- so bridge
 * belongs there. It used to be reachable only through a stack-level mode
 * picker that said the same thing as the table beside it, which is what made
 * that screen confusing enough to delete.
 *
 * A row already on it keeps it, so opening the page never silently drops a
 * choice something else made.
 */
export const perServiceModes = (current: string, serviceCount = 1) =>
  MODES.filter((m) => m !== 'bridge' || m === current || serviceCount <= 1);

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
 * reach the rest of its own stack. A service on a MODE gets exactly that one
 * row: a mode is the whole answer, and showing it as a row is what makes it
 * visible and changeable. Never an empty list, which reads as "broken".
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
  networks: { network: string; service: string; ip: string; mac: string }[];
  modes: Record<string, string>;
} {
  const networks: { network: string; service: string; ip: string; mac: string }[] = [];
  const modes: Record<string, string> = {};
  for (const [svc, rows] of Object.entries(edits)) {
    let placed = false;
    for (const r of rows) {
      if (!r.network) continue;
      placed = true;
      if (isMode(r.network)) {
        modes[svc] = r.network;
        continue;
      }
      networks.push({
        network: r.network,
        service: svc,
        ip: (r.ip ?? '').trim(),
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
 * Choosing a MODE replaces the whole list: a container on the host's stack, on
 * podman's default bridge, or on nothing does not also hold other interfaces.
 * That is correct and was invisible -- the private row just disappeared -- so
 * the caller is told, and can say so on screen.
 */
export function setInterface(
  rows: Iface[],
  i: number,
  field: 'network' | 'ip' | 'mac',
  value: string,
): { rows: Iface[]; replaced: Iface[] } {
  if (field === 'network' && isMode(value)) {
    const replaced = rows.filter((_, j) => j !== i).filter((r) => r.network);
    return { rows: [{ network: value }], replaced };
  }
  const out = rows.map((r, j) => (j === i ? { ...r, [field]: value } : r));
  // A different network invalidates an address from the old one.
  if (field === 'network') out[i] = { ...out[i], ip: '' };
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
 * A single-service stack has nobody to be cut off from, so it never warns.
 */
export function isolatedServices(edits: Record<string, Iface[]>): string[] {
  const names = Object.keys(edits);
  if (names.length < 2) return [];
  const reachable = (svc: string) =>
    new Set((edits[svc] ?? []).map((r) => r.network).filter((n) => n && n !== 'none'));
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
