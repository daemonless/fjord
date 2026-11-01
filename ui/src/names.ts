// Names for apps (radarr.apps.lan) through an installed names provider, a
// reverse proxy from the store or anything else that serves the names API
// (docs/names-provider.md). The daemon pairs with it at install and talks
// to it; this side decides what a browser should open, per device: a name
// only once this browser trusts the provider and can resolve its names,
// else the address as before.

import { get, writable } from 'svelte/store';
import { appUrl, type AppUrlStack } from './appUrl';
import { serviceLinks } from './serviceLinks';

export type AppName = { id: string; name: string; url: string; access?: string };
export type NamesState = {
  /** The stack that gives names; unset when nothing installed does. */
  provider?: string;
  setupToken?: string;
  ready: boolean;
  domain?: string;
  /** The provider's own name: loading it over HTTPS proves this device trusts it. */
  trustName?: string;
  trustUrl?: string;
  names?: Record<string, AppName>;
  /** The provider refuses fjord's key: disconnected in its Settings. */
  disconnected?: boolean;
  error?: string;
  /** Labels every other host on the provider holds: another fjord's, or
   *  made by hand. A suggested name avoids them. */
  taken?: string[];
  /** Linked by hand (Settings, Names): a proxy on another machine. */
  linked?: boolean;
  /** Where a linked proxy's name resolves: a stack here at one of them is it. */
  linkAddrs?: string[];
  /** This fjord, as providers see it. */
  self?: { id: string; name: string };
};

/** trusted: names load clean here. untrusted: they resolve but this browser
 *  does not trust the provider yet. no-dns: this device cannot find them. */
export type DeviceTrust = 'unknown' | 'trusted' | 'untrusted' | 'no-dns';

export const names = writable<NamesState>({ ready: false });
export const deviceTrust = writable<DeviceTrust>('unknown');

/** What Open uses in this browser once an app has a name: the last one picked
 *  from the menu beside Open. Names by default -- that is what they are for. */
export type OpenBy = 'name' | 'address';
const openByKey = 'fjord.openBy';
function storedOpenBy(): OpenBy {
  try {
    return localStorage.getItem(openByKey) === 'address' ? 'address' : 'name';
  } catch {
    return 'name';
  }
}
export const openBy = writable<OpenBy>(storedOpenBy());
openBy.subscribe((v) => {
  try {
    localStorage.setItem(openByKey, v);
  } catch {}
});

// A provider that is installed but not ready is looked at again every few
// seconds: it is still starting (no answer yet), or its setup is being done
// in another tab. Without this the Finish setup row only showed after a
// reload, and the person went looking for the token in the .env.
let again: ReturnType<typeof setTimeout> | undefined;
let againMs = 5000;

// fjord itself did not answer (restarting, a blip): look again, backing
// off to a minute. Returning without a timer stopped every later look, and
// the row stayed as it was until a reload (jupiter, 2026-10-28).
function lookAgainLater() {
  again = setTimeout(loadNames, againMs);
  againMs = Math.min(againMs * 2, 60000);
}

export async function loadNames(): Promise<NamesState> {
  clearTimeout(again);
  again = undefined;
  try {
    const res = await fetch('/api/names');
    if (!res.ok) {
      lookAgainLater();
      return get(names);
    }
    const p: NamesState = await res.json();
    names.set(p);
    // Every look, both ways: a tab left open notices DNS for the ending
    // going away (names open direct) and coming back, without a reload.
    if (p.ready && p.trustName) checkTrust(p.trustName);
    // Not ready and still worth asking: a provider that does not answer
    // yet, or one that could not be looked at. Every 5 s while it is being
    // set up; a lasting error backs off to a minute.
    const wait = p.provider && !p.ready && !p.disconnected;
    const stuck = !p.provider && !!p.error;
    if (wait || stuck) {
      again = setTimeout(loadNames, againMs);
      againMs = stuck ? Math.min(againMs * 2, 60000) : 5000;
    } else {
      againMs = 5000;
      // Ready: a slow look, so a new ending set in the provider's Settings
      // (every name changes) or a disconnect shows without a reload.
      if (p.ready) again = setTimeout(loadNames, 60000);
    }
    return p;
  } catch {
    lookAgainLater();
    return get(names);
  }
}

/** The provider's own trust check: CORS-open, answers on every name. HTTPS
 *  loading at all means trusted; plain HTTP loading means the name resolves.
 *  A name nothing resolves can hang in the resolver: 4 s is an answer, and
 *  both probes go out together so the answer takes one wait, not two.
 *  The plain probe goes to a sibling name under the same ending (the record
 *  is a wildcard): the provider sends HSTS on its own name, and once a
 *  browser has seen that it turns the plain probe into HTTPS, which fails
 *  on a device that stopped trusting the root (a new ending made a new
 *  one) and read as "no DNS" when the truth was "not trusted". */
export async function checkTrust(trustName: string): Promise<DeviceTrust> {
  const probe = (url: string) =>
    fetch(url, { mode: 'cors', cache: 'no-store', signal: AbortSignal.timeout(4000) }).then(() => true, () => false);
  const sibling = trustName.replace(/^[^.]+\./, 'fjord-check.');
  // fjord's own page is plain HTTP, so the plain probe is not mixed content.
  const [https, http] = await Promise.all([probe(`https://${trustName}/trust/check`), probe(`http://${sibling}/trust/check`)]);
  const t: DeviceTrust = https ? 'trusted' : http ? 'untrusted' : 'no-dns';
  deviceTrust.set(t);
  return t;
}

type NamedStack = AppUrlStack & {
  name: string;
  services?: { name: string; address?: string }[];
  status?: AppUrlStack['status'] & { state?: string };
};

/** One app a name can point at. A stack of several apps (crafting-apps) has
 *  one per app, keyed "<stack>/<service>" and named after the service; any
 *  other stack has one, keyed and named after the stack. */
export type NameTarget = { key: string; label: string; url: string; address: string; port: number; https: boolean; service?: string };

function target(key: string, label: string, url: string, service?: string): NameTarget {
  const u = new URL(url);
  const https = u.protocol === 'https:';
  return { key, label: nameLabel(label), url, address: u.hostname, port: Number(u.port || (https ? 443 : 80)), https, service };
}

export function nameTargets(stack: NamedStack | null): NameTarget[] {
  if (!stack) return [];
  const env: Record<string, string> = {};
  for (const line of (stack.env || '').split('\n')) {
    const eq = line.indexOf('=');
    if (eq > 0) env[line.slice(0, eq).trim()] = line.slice(eq + 1).trim();
  }
  const links = serviceLinks(stack.compose || '', env, stack.services || [], location.hostname);
  if (links.length > 1) return links.map((l) => target(`${stack.name}/${l.service}`, l.service, l.url, l.service));
  const raw = appUrl(stack);
  return raw ? [target(stack.name, stack.name, raw)] : [];
}

/** Where one app opens: its name, unless this browser picked addresses or
 *  this device cannot find the names at all (no DNS record yet): a link
 *  that cannot load is no link. The shield beside it says why. */
export function linkUrl(p: NamesState, key: string, address: string, by: OpenBy, trust: DeviceTrust = 'unknown'): string {
  const n = p.names?.[key];
  return n && by === 'name' && trust !== 'no-dns' ? n.url : address;
}

/** Where Open goes: the stack's main app (the first), by name or address.
 *  The provider itself, until its setup is done, opens with its setup
 *  token: every way in from fjord lands verified, none at "paste the
 *  token from the log". */
export function openUrl(stack: NamedStack | null, p: NamesState, by: OpenBy, trust: DeviceTrust = 'unknown'): string {
  const raw = appUrl(stack);
  if (!stack || !raw) return raw;
  if (stack.name === p.provider && !p.ready && p.setupToken) return `${raw.replace(/\/$/, '')}/#token=${p.setupToken}`;
  const own = trust === 'no-dns' ? '' : providerUrl(stack, p, by);
  if (own) return own;
  const first = nameTargets(stack)[0];
  return first ? linkUrl(p, first.key, raw, by, trust) : raw;
}

/** The provider at its own name (https://proxy.apps.lan, no port: its own
 *  name serves its page on 443) once it is set up and this browser opens
 *  by name; "" for any other stack. Its row and header read like every
 *  other app's, not an address among names. */
export function providerUrl(stack: NamedStack | null, p: NamesState, by: OpenBy): string {
  if (!stack || stack.name !== p.provider || !p.ready || !p.trustName || by !== 'name') return '';
  return appUrl(stack) ? `https://${p.trustName}` : '';
}

/** The names a stack has, one per app. */
export function stackNames(stack: NamedStack | null, p: NamesState): AppName[] {
  return nameTargets(stack)
    .map((t) => p.names?.[t.key])
    .filter((n): n is AppName => !!n);
}

/** Open by name in this browser: the app has a name and names are what Open uses. */
export function opensByName(stack: NamedStack | null, p: NamesState, by: OpenBy): boolean {
  return by === 'name' && (stackNames(stack, p).length > 0 || providerUrl(stack, p, by) !== '');
}

/** The provider's setup page, signed in with the token fjord made. */
export function setupUrl(p: NamesState, stacks: (AppUrlStack & { name: string })[]): string {
  const s = stacks.find((x) => x.name === p.provider) || null;
  const base = appUrl(s);
  return base && p.setupToken ? `${base.replace(/\/$/, '')}/#token=${p.setupToken}` : base;
}

/** The provider's own Settings page, where it is reconnected. */
export function providerSettingsUrl(p: NamesState, stacks: (AppUrlStack & { name: string })[]): string {
  const s = stacks.find((x) => x.name === p.provider) || null;
  const base = appUrl(s);
  return base ? `${base.replace(/\/$/, '')}/settings` : '';
}

/** The trust page: by name when this device finds the provider's names, else
 *  by address, which works before DNS is set up. */
export function trustPage(p: NamesState, stacks: (AppUrlStack & { name: string })[], trust: DeviceTrust = 'unknown'): string {
  if (trust === 'untrusted' && p.trustUrl) return p.trustUrl;
  const s = stacks.find((x) => x.name === p.provider) || null;
  const base = appUrl(s);
  if (!base) return p.trustUrl || '';
  const u = new URL(base);
  return `http://${u.hostname}/trust`;
}

/** The stack (or stack/app) that has this label, or "another fjord" when
 *  one linked to the same provider holds it (p.taken): the wizard says so
 *  and holds Install, instead of the provider refusing after the install. */
export function nameOwner(label: string, p: NamesState): string | undefined {
  return Object.entries(p.names || {}).find(([, n]) => n.name === label)?.[0] ?? (p.taken?.includes(label) ? 'another fjord' : undefined);
}

/** base, or base-2, base-3 ... until one nothing on the provider has: no
 *  app of fjord's, no other fjord's, nothing made by hand. A second copy of
 *  an app must not be offered the first one's name. */
export function freeName(base: string, p: NamesState, also: Set<string> = new Set()): string {
  let l = base;
  for (let n = 2; nameOwner(l, p) || also.has(l); n++) l = `${base}-${n}`;
  return l;
}

/** The targets with labels free on the provider, and free of each other:
 *  two unnamed copies given names together get base and base-2. */
export function freeLabels(targets: NameTarget[], p: NamesState): NameTarget[] {
  const used = new Set<string>();
  return targets.map((t) => {
    const label = freeName(t.label, p, used);
    used.add(label);
    return { ...t, label };
  });
}

export function nameLabel(s: string): string {
  return s
    .toLowerCase()
    .trim()
    .replace(/[\s._]+/g, '-')
    .replace(/[^a-z0-9-]/g, '')
    .replace(/^-+|-+$/g, '');
}

/** Apps that could have a name and have none yet (not the provider itself).
 *  A stopped stack is left out: the provider probes an app before naming it,
 *  so naming one that is not running always failed, and the prompt offered
 *  it again on every visit. It is offered once it runs. */
export function unnamed(stacks: NamedStack[], p: NamesState): NameTarget[] {
  const stopped = (s: NamedStack) => !!s.status?.state && s.status.state !== 'running' && s.status.state !== 'partial';
  return stacks.filter((s) => s.name !== p.provider && !stopped(s)).flatMap((s) => nameTargets(s).filter((t) => !p.names?.[t.key]));
}

export type NameResult = { stack: string; host?: AppName; error?: string };

/** Give names. label only applies to a stack with one app. */
export async function addNames(list: ({ target: NameTarget } | { stack: NamedStack; label?: string })[]): Promise<NameResult[]> {
  // A label someone typed goes as typed (the provider refuses a taken one in
  // its own words); the rest are suggestions, made free first.
  const state = get(names);
  const targets = list.flatMap((x) => {
    if ('target' in x) return freeLabels([x.target], state);
    const ts = nameTargets(x.stack);
    return ts.length === 1 && x.label ? [{ ...ts[0], label: nameLabel(x.label) }] : freeLabels(ts, state);
  });
  const wanted = targets.map((t) => ({ stack: t.key, label: t.label, address: t.address, port: t.port, https: t.https }));
  const res = await fetch('/api/names', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ names: wanted }),
  });
  if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
  const out: { results: NameResult[] } = await res.json();
  await loadNames();
  return out.results;
}

/** Take names away: one app's ("crafting-apps/photocraft") or a stack's,
 *  which takes all of its apps' names. */
export async function removeName(key: string): Promise<void> {
  const res = await fetch(`/api/names/${key.split('/').map(encodeURIComponent).join('/')}`, { method: 'DELETE' });
  if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
  await loadNames();
}

/** Change one app's name: the host keeps its target, only the label is new.
 *  The provider refuses a taken label in its own words. */
export async function renameName(key: string, label: string): Promise<AppName> {
  const res = await fetch(`/api/names/${key.split('/').map(encodeURIComponent).join('/')}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ label }),
  });
  if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
  const host: AppName = await res.json();
  await loadNames();
  return host;
}

/** Link a reverse proxy elsewhere: the link from its Connected apps page
 *  (or an address plus the code). */
export async function linkProvider(link: string, code = ''): Promise<NamesState> {
  const res = await fetch('/api/names/link', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ link, code }) });
  if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
  const p: NamesState = await res.json();
  names.set(p);
  loadNames();
  return p;
}

export async function unlinkProvider(): Promise<void> {
  const res = await fetch('/api/names/link', { method: 'DELETE' });
  if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
  await loadNames();
}

/** Rename this fjord as providers list it ("fjord on <name>"). */
export async function renameSelf(name: string): Promise<void> {
  const res = await fetch('/api/names/self', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }) });
  if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
  await loadNames();
}
