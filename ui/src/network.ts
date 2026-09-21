// Which network a stack's containers are on, read from its compose.
//
// The sidebar groups by this, so it answers "what would I break by touching
// this VLAN" -- the label is the network as compose declares it, not whatever
// the engine happens to report at runtime.

import * as yaml from 'js-yaml';
import ipaddr from 'ipaddr.js';

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

/**
 * Why an address can't be used on a network, or "" if it can.
 *
 * The daemon checks this too and is the authority -- it knows about
 * reservations the browser can't see. But it only gets to speak once Install
 * is pressed, and by then the wizard is gone and everything typed into it with
 * it. The rules below need nothing the UI doesn't already have, so they run
 * while the field is being filled in: same wording as the daemon's refusal, so
 * the two never look like different complaints.
 */
export function addressProblem(
  addr: string,
  net: { subnet?: string; gateway?: string; addressSource?: string } | undefined,
): string {
  const a = (addr || '').trim();
  if (!a) return '';
  // isValidFourPartDecimal, NOT isValid: isValid("1") is true -- it reads a
  // bare 1 as 0.0.0.1, which is the typo that reached a jail as
  // `ifconfig sb_x:1/24` and left it answering nothing. The strict form also
  // refuses leading zeros and 0x/1e forms, which is what the daemon's
  // net.ParseIP does.
  if (!ipaddr.IPv4.isValidFourPartDecimal(a)) return `${a} is not an IPv4 address`;
  const subnet = net?.subnet || '';
  if (!ipaddr.IPv4.isValidCIDRFourPartDecimal(subnet)) return ''; // segment unknown
  const ip = ipaddr.IPv4.parse(a);
  const range = ipaddr.IPv4.parseCIDR(subnet);
  if (!ip.match(range)) return `${a} is not in ${subnet}`;
  // A /32 is one host, and its network and broadcast addresses ARE that host.
  // Same guard the daemon uses, so the two agree on a single-address network.
  if (range[1] < 32) {
    if (ip.toString() === ipaddr.IPv4.networkAddressFromCIDR(subnet).toString()) {
      return `${a} is the network address of ${subnet}, not a host in it`;
    }
    if (ip.toString() === ipaddr.IPv4.broadcastAddressFromCIDR(subnet).toString()) {
      return `${a} is the broadcast address of ${subnet}, not a host in it`;
    }
  }
  // The gateway is the one rule that isn't universal. On a network the engine
  // allocates on, the address reported as the gateway is also the first one it
  // hands out -- appjail's ajnet gives 10.0.0.1 as both -- so refusing it here
  // would block an address that works. Everything above still holds: appjail's
  // own range stops short of the network and broadcast addresses too.
  if (
    net?.addressSource !== 'engine' &&
    net?.gateway &&
    ipaddr.IPv4.isValidFourPartDecimal(net.gateway) &&
    ipaddr.IPv4.parse(net.gateway).toString() === ip.toString()
  ) {
    return `${a} is the gateway for ${subnet}`;
  }
  return '';
}

/**
 * The addresses a stack may actually take on a network, as "first – last".
 *
 * The subnet on its own doesn't answer "what can I type here": three of its
 * addresses are spoken for, and which three depends on the network. This is
 * the same set addressProblem accepts, said forwards.
 *
 * "" when there is nothing useful to say -- no segment, or a /31 or /32, which
 * have no host range to speak of.
 */
export function usableRange(
  net: { subnet?: string; gateway?: string; addressSource?: string } | undefined,
): string {
  const subnet = net?.subnet || '';
  if (!ipaddr.IPv4.isValidCIDRFourPartDecimal(subnet)) return '';
  if (ipaddr.IPv4.parseCIDR(subnet)[1] >= 31) return '';
  let first = step(ipaddr.IPv4.networkAddressFromCIDR(subnet), 1);
  let last = step(ipaddr.IPv4.broadcastAddressFromCIDR(subnet), -1);
  // The gateway is out too, on the networks where addressProblem enforces it.
  // It sits at one end almost always, so trimming the end it's on keeps the
  // range contiguous and honest; a gateway in the middle stays inside it, and
  // the field says so when someone types it.
  if (net?.addressSource !== 'engine' && net?.gateway && ipaddr.IPv4.isValidFourPartDecimal(net.gateway)) {
    const gw = ipaddr.IPv4.parse(net.gateway).toString();
    if (gw === first.toString()) first = step(first, 1);
    else if (gw === last.toString()) last = step(last, -1);
  }
  return `${first.toString()} – ${last.toString()}`;
}

/** The address `by` steps along from a. ipaddr.js has no "next address". */
function step(a: ipaddr.IPv4, by: number): ipaddr.IPv4 {
  const n = a.octets.reduce((acc, o) => acc * 256 + o, 0) + by;
  return new ipaddr.IPv4([(n >>> 24) & 255, (n >>> 16) & 255, (n >>> 8) & 255, n & 255]);
}
