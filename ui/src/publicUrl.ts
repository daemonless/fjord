// A public_url variable is the address the app is opened at (Vikunja's
// public URL, for links in the mail it sends). The wizard fills it from
// where the install puts the app; a hand-typed one is easy to get wrong, and
// "f/" crash-looped Vikunja.

export type PublicUrlInput = {
  webPort: string; // the manifest's x-fjord web_port: "8080" or "${WEB_PORT}"
  https: boolean;
  values: Record<string, string>;
  host: string; // the name this page was opened at
  ownAddress: boolean; // the app gets an address of its own (a LAN network)
  ownIP: string; // that address, when one is pinned
  insidePort: string; // the app's own port, used at its own address
};

/** suggestedPublicUrl: where the app will answer, no trailing slash (some
 *  apps refuse one), or "" when that is not known
 *  yet (its own address comes from DHCP at install). */
export function suggestedPublicUrl(o: PublicUrlInput): string {
  const scheme = o.https ? 'https' : 'http';
  const ref = o.webPort.match(/^\$\{([A-Za-z_][A-Za-z0-9_]*)/);
  const hostPort = ref ? (o.values[ref[1]] ?? '') : o.webPort;
  if (o.ownAddress) {
    const port = o.insidePort || hostPort;
    return o.ownIP && port ? `${scheme}://${o.ownIP}:${port}` : '';
  }
  if (!/^\d{2,5}$/.test(hostPort) || !o.host) return '';
  return `${scheme}://${o.host}:${hostPort}`;
}

/** publicUrlProblem: why a typed address will not do, or "". Empty is the
 *  field's own business (optional or required). */
export function publicUrlProblem(v: string): string {
  if (!v) return '';
  if (!/^https?:\/\//i.test(v)) return 'Start with http:// or https://';
  try {
    if (!new URL(v).hostname) return 'Needs a host name or address';
  } catch {
    return 'Not a valid address';
  }
  return '';
}
