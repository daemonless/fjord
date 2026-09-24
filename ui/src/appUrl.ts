// One answer to "where does this app live", shared by the dashboard tiles and
// the stack detail. It was two: the dashboard read only ipv4_address out of
// the compose, so a DHCP address -- which exists nowhere but at runtime --
// was invisible to it and every such stack got a host URL that times out.

import { expandVars } from './expand';

export type AppUrlStack = {
  compose?: string;
  /** The address to open this stack at, decided by the daemon: only it knows
   *  each service's address ON EACH network and which of those a browser can
   *  reach. Guessing from the stack's first network picked the private
   *  segment for a stack whose app is on the LAN. */
  linkHost?: string;
  env?: string;
  network?: string;
  /** The first network puts the container on a real segment (daemon says so). */
  ownAddress?: boolean;
  status?: {
    containers?: { address?: string; ports?: { hostPort: number; containerPort: number; protocol?: string }[] }[];
  };
};

export function appUrl(stack: AppUrlStack | null): string {
  if (!stack) return '';
  const c = stack.compose || '';
  // An address is only somewhere the BROWSER can go when the network puts the
  // container on a real segment. Every container has an address -- podman's
  // own bridge hands out 10.88/10.89, and so does a private network someone
  // made on purpose -- but those are NAT behind the host: reachable from the
  // host, not from here. Such stacks publish ports, so the host is the right
  // answer and the container's address is the wrong one. The daemon decides,
  // from the network's own definition; "is it attached" gets private
  // networks wrong.
  const attached = !!stack.ownAddress;
  // The RUNTIME address first: an auto-assigned one exists nowhere else. The
  // compose only carries ipv4_address when the user pinned it, which leaves
  // every DHCP stack with nothing to build a link from.
  const running = attached ? (stack.status?.containers || []).find((x: any) => x.address)?.address : undefined;
  const ip = stack.linkHost || running || (attached ? (c.match(/ipv4_address:\s*([0-9.]+)/) || [])[1] : undefined);
  // Attached but address-less: the network gave it none, so there is no link
  // to offer -- the host would just time out. Say why instead (see noAddress).
  if (!ip && attached) return '';
  const host = ip || location.hostname;

  const env: Record<string, string> = {};
  for (const line of (stack.env || '').split('\n')) {
    const eq = line.indexOf('=');
    if (eq > 0) env[line.slice(0, eq).trim()] = line.slice(eq + 1).trim();
  }
  const resolve = (v: string) => expandVars(v, env);

  // x-fjord web hint -- derived from the image's cit config (the same
  // port + https flag dbuild tests against), so nothing is guessed. May be
  // a "${VAR}" reference resolved via .env. The only source for host-net
  // stacks, which publish nothing.
  const hinted = resolve((c.match(/web_port:\s*["']?([^\s"']+)/) || [])[1] || '');
  if (/^\d{2,5}$/.test(hinted)) {
    const scheme = /web_https:\s*true/.test(c) ? 'https' : 'http';
    return `${scheme}://${host}:${hinted}`;
  }

  // No hint: prefer the RUNNING container's actual published ports (from the
  // status API) -- the saved compose can be ahead of reality, since edits
  // only apply on a recreate. Scheme http: https is only ever hint-declared.
  // On a macvlan IP nothing is published (hostPort 0): the app answers on
  // its container port at that IP. Otherwise only real published ports count.
  let ports: string[] = (stack.status?.containers ?? [])
    .flatMap((ct) => ct.ports ?? [])
    .filter((p) => !p.protocol || p.protocol === 'tcp')
    .map((p) => String(ip ? p.containerPort || p.hostPort : p.hostPort))
    .filter((p) => p !== '0');

  // Fallback (stack stopped, or on its own address and publishing nothing):
  // the compose's ports list items, resolving ${VAR} from .env. Anchored to
  // "- host:container" lines so a MAC address (00:00) or an IP never reads
  // as a port. At the container's own address only the container side is
  // listening: notes' web on the LAN linked to :8001, its old host port.
  if (!ports.length) {
    ports = [...c.matchAll(/^\s*-\s*["']?([\w${}.]+):(\d{2,5})(?:\/(tcp|udp))?["']?\s*$/gm)]
      .filter((m) => !m[3] || m[3] === 'tcp')
      .map((m) => (ip ? m[2] : resolve(m[1])))
      .filter((h) => /^\d{2,5}$/.test(h));
  }
  return ports.length ? `http://${host}:${ports[0]}` : '';
}
