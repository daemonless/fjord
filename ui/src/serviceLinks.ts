// Each web service of a stack, where to open it. A stack of several apps
// (crafting-apps: seven, each on its own LAN address) had one Open link, to
// the first; the rest could not be reached from the page.

import * as yaml from 'js-yaml';
import { expandVars } from './expand';

export type ServiceLink = { service: string; url: string };

type Svc = { name: string; address?: string };

// "8080:80", "${WEB_PORT}:80/tcp", "127.0.0.1:8080:80" -> [host, container];
// udp and bare container ports publish nothing to open.
function split(spec: string, env: Record<string, string>): [string, string] | null {
  const s = expandVars(String(spec).trim(), env);
  if (/\/udp$/.test(s)) return null;
  const parts = s.replace(/\/tcp$/, '').split(':');
  if (parts.length < 2) return null;
  const host = parts[parts.length - 2];
  const container = parts[parts.length - 1];
  return /^\d{1,5}$/.test(host) && /^\d{1,5}$/.test(container) ? [host, container] : null;
}

/** serviceLinks: one link per service that publishes a port, in the
 *  compose's order. A service on its own address (fjord parks its ports under
 *  x-fjord-published) is opened there, at the app's own port; the rest at
 *  this host's published port. */
export function serviceLinks(compose: string, env: Record<string, string>, services: Svc[], host: string): ServiceLink[] {
  let doc: any;
  try {
    doc = yaml.load(compose);
  } catch {
    return [];
  }
  const out: ServiceLink[] = [];
  for (const [name, svc] of Object.entries<any>(doc?.services ?? {})) {
    const parked = svc?.['x-fjord-published'];
    if (Array.isArray(parked) && parked.length) {
      const addr = services.find((s) => s.name === name)?.address;
      const p = parked.map((x: string) => split(x, env)).find(Boolean);
      if (addr && p) out.push({ service: name, url: `http://${addr}:${p[1]}` });
      continue;
    }
    const ports = Array.isArray(svc?.ports) ? svc.ports : [];
    const p = ports.map((x: any) => split(typeof x === 'object' ? `${x.published}:${x.target}` : x, env)).find(Boolean);
    if (p && host) out.push({ service: name, url: `http://${host}:${p[0]}` });
  }
  return out;
}
