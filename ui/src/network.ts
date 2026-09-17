// Which network a stack's containers are on, read from its compose.
//
// The sidebar groups by this, so it answers "what would I break by touching
// this VLAN" -- the label is the network as compose declares it, not whatever
// the engine happens to report at runtime.

import * as yaml from 'js-yaml';

export const HOST_NETWORK = 'host';
export const DEFAULT_NETWORK = 'bridge';

// Parsing YAML for every stack on every reactive recompute is wasteful and the
// compose text is a stable key, so results are memoised on it.
const cache = new Map<string, string[]>();

/**
 * The networks a compose file puts its services on, sorted and de-duplicated.
 *
 * - `network_mode: host` (or `service:`/`container:`) wins outright: those
 *   services are not on a named network at all.
 * - Otherwise the service-level `networks:` keys, which is where compose names
 *   what a container joins. The top-level `networks:` block only declares them
 *   (often `external: true`), and a stack can declare one it never attaches, so
 *   it is not a reliable answer on its own.
 * - Nothing named means compose's implicit default bridge.
 */
export function stackNetworks(composeYAML: string | undefined): string[] {
  const text = composeYAML || '';
  const hit = cache.get(text);
  if (hit) return hit;

  let out: string[];
  try {
    const doc: any = yaml.load(text);
    const services = doc?.services;
    const found = new Set<string>();
    let host = false;

    for (const key of Object.keys(services || {})) {
      const svc = services[key];
      if (!svc || typeof svc !== 'object') continue;

      const mode = typeof svc.network_mode === 'string' ? svc.network_mode.trim() : '';
      if (mode) {
        // host, none, service:x, container:x -- all mean "no named network".
        found.add(mode === 'host' ? HOST_NETWORK : mode);
        host = true;
        continue;
      }

      // networks: either a mapping (with per-service options like ipv4_address)
      // or a plain list of names. Both are valid compose.
      const nets = svc.networks;
      if (Array.isArray(nets)) {
        for (const n of nets) if (typeof n === 'string' && n.trim()) found.add(n.trim());
      } else if (nets && typeof nets === 'object') {
        for (const n of Object.keys(nets)) if (n.trim()) found.add(n.trim());
      }
    }

    if (found.size === 0) found.add(DEFAULT_NETWORK);
    // A stack mixing host-mode and named networks is unusual but legal; listing
    // both is more honest than picking one.
    out = [...found].sort((a, b) => a.localeCompare(b));
    void host;
  } catch {
    // Unparseable compose (mid-edit, or hand-written oddity): don't guess.
    out = ['unknown'];
  }

  cache.set(text, out);
  return out;
}

/** One label for the sidebar bucket: joined when a stack spans networks. */
export function networkLabel(composeYAML: string | undefined): string {
  return stackNetworks(composeYAML).join(', ');
}

/**
 * A random MAC fjord can hand out safely.
 *
 * The first octet is fixed to 0x02: bit 0 clear makes it unicast, bit 1 set
 * makes it locally administered -- the range set aside for addresses nobody
 * bought from the IEEE, so it cannot collide with a real NIC's burned-in one.
 * The rest is random, which is enough: the point is a stable identity for a
 * DHCP reservation, not global uniqueness.
 */
export function randomMAC(): string {
  const b = new Uint8Array(5);
  crypto.getRandomValues(b);
  return ['02', ...b].map((x) => Number(x).toString(16).padStart(2, '0')).join(':');
}
