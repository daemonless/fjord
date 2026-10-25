import { describe, it, expect, vi } from 'vitest';
import { openUrl, nameTargets, nameLabel, unnamed, trustPage, opensByName, freeName, freeLabels, nameOwner, type NamesState } from './names';

// serviceLinks and appUrl fall back to the page's host.
vi.stubGlobal('location', { hostname: '192.168.4.103' });

const radarr = {
  name: 'radarr',
  compose: 'services:\n  radarr:\n    ports:\n      - "7878:7878"\nx-fjord:\n  web_port: "7878"\n',
  linkHost: '192.168.4.103',
  state: { origin: { type: 'catalog' } },
};
const ready: NamesState = {
  ready: true,
  provider: 'reverse-proxy',
  domain: 'apps.lan',
  names: { radarr: { id: 'a1', name: 'radarr', url: 'https://radarr.apps.lan' } },
};

describe('openUrl', () => {
  it('opens the name, or the address when this browser picked addresses', () => {
    expect(openUrl(radarr, ready, 'name')).toBe('https://radarr.apps.lan');
    expect(openUrl(radarr, ready, 'address')).toBe('http://192.168.4.103:7878');
  });
  it('falls back to the address on a device that cannot find the names', () => {
    expect(openUrl(radarr, ready, 'name', 'no-dns')).toBe('http://192.168.4.103:7878');
    expect(openUrl(radarr, ready, 'name', 'untrusted')).toBe('https://radarr.apps.lan');
    expect(opensByName(radarr, ready, 'name')).toBe(true); // the shield still says why
  });
  it('keeps the address for an app with no name', () => {
    expect(openUrl({ ...radarr, name: 'sonarr' }, ready, 'name')).toBe('http://192.168.4.103:7878');
  });
  it('opens the provider itself with its setup token until its setup is done', () => {
    const proxy = { ...radarr, name: 'reverse-proxy', compose: 'services:\n  reverse-proxy:\n    ports:\n      - "8443:8443"\nx-fjord:\n  web_port: "8443"\n  web_https: true\n' };
    const starting: NamesState = { ready: false, provider: 'reverse-proxy', setupToken: 'T0K' };
    expect(openUrl(proxy, starting, 'name')).toBe('https://192.168.4.103:8443/#token=T0K');
    expect(openUrl(proxy, { ...ready, names: {} }, 'name')).toBe('https://192.168.4.103:8443');
    expect(openUrl(radarr, starting, 'name')).toBe('http://192.168.4.103:7878');
  });
  it('opens the provider at its own name once it is set up', () => {
    const proxy = { ...radarr, name: 'reverse-proxy', compose: 'services:\n  reverse-proxy:\n    ports:\n      - "8443:8443"\nx-fjord:\n  web_port: "8443"\n  web_https: true\n' };
    const p: NamesState = { ...ready, trustName: 'proxy.apps.lan' };
    expect(openUrl(proxy, p, 'name')).toBe('https://proxy.apps.lan');
    expect(openUrl(proxy, p, 'address')).toBe('https://192.168.4.103:8443');
    expect(opensByName(proxy, p, 'name')).toBe(true);
    expect(openUrl(proxy, ready, 'name')).toBe('https://192.168.4.103:8443');
  });
  it('says when Open goes by name, for the trust mark', () => {
    expect(opensByName(radarr, ready, 'name')).toBe(true);
    expect(opensByName(radarr, ready, 'address')).toBe(false);
    expect(opensByName({ ...radarr, name: 'sonarr' }, ready, 'name')).toBe(false);
  });
});

describe('freeName', () => {
  it('skips the names apps already have, and says whose a name is', () => {
    const p: NamesState = { ...ready, names: { ...ready.names, 'radarr-2': { id: 'a2', name: 'radarr-2', url: 'https://radarr-2.apps.lan' } } };
    expect(freeName('radarr', p)).toBe('radarr-3');
    expect(freeName('sonarr', p)).toBe('sonarr');
    expect(nameOwner('radarr', p)).toBe('radarr');
    expect(nameOwner('sonarr', p)).toBeUndefined();
  });
  // Another fjord's radarr, or one made by hand on the proxy, is taken too.
  it('skips names other fjords and hand-made hosts hold', () => {
    const p: NamesState = { ...ready, names: {}, taken: ['radarr', 'sonarr-2'] };
    expect(freeName('radarr', p)).toBe('radarr-2');
    expect(freeName('sonarr', p)).toBe('sonarr');
    expect(nameOwner('radarr', p)).toBeUndefined();
  });
  it('gives two copies named together different names', () => {
    const p: NamesState = { ...ready, names: {}, taken: ['radarr'] };
    const t = (key: string) => ({ key, label: 'radarr', address: '192.168.4.103', port: 7878, https: false, url: 'http://192.168.4.103:7878' });
    expect(freeLabels([t('radarr'), t('radarr-copy')], p).map((x) => x.label)).toEqual(['radarr-2', 'radarr-3']);
  });
});

describe('nameTargets', () => {
  it('points the name where a browser opens the app', () => {
    expect(nameTargets(radarr)).toMatchObject([{ key: 'radarr', label: 'radarr', address: '192.168.4.103', port: 7878, https: false }]);
  });
  // crafting-apps: thirteen apps, each on its own address. Each gets a name.
  it('gives each app of a stack of several its own name', () => {
    const crafting = {
      name: 'crafting-apps',
      compose: 'services:\n  photocraft:\n    x-fjord-published:\n      - "8080:80"\n  vectorcraft:\n    x-fjord-published:\n      - "8081:80"\nx-fjord:\n  web_port: "8080"\n',
      services: [{ name: 'photocraft', address: '192.168.4.108' }, { name: 'vectorcraft', address: '192.168.4.109' }],
      linkHost: '192.168.4.108',
      ownAddress: true,
      state: { origin: { type: 'catalog' } },
    };
    expect(nameTargets(crafting).map((t) => [t.key, t.label, t.address, t.port])).toEqual([
      ['crafting-apps/photocraft', 'photocraft', '192.168.4.108', 80],
      ['crafting-apps/vectorcraft', 'vectorcraft', '192.168.4.109', 80],
    ]);
    const p = { ...ready, names: { 'crafting-apps/photocraft': { id: 'x', name: 'photocraft', url: 'https://photocraft.apps.lan' } } };
    expect(openUrl(crafting, p, 'name')).toBe('https://photocraft.apps.lan');
    expect(unnamed([crafting], p).map((t) => t.key)).toEqual(['crafting-apps/vectorcraft']);
  });
});

describe('nameLabel', () => {
  it('makes a label a provider accepts', () => {
    expect(nameLabel('Radarr 4K')).toBe('radarr-4k');
    expect(nameLabel('my_app.v2')).toBe('my-app-v2');
    expect(nameLabel('-x-')).toBe('x');
  });
});

describe('unnamed', () => {
  it('leaves out named apps, the provider itself and apps with no page', () => {
    const db = { name: 'pg', compose: 'services: {}\n', state: { origin: { type: 'catalog' } } };
    const self = { ...radarr, name: 'reverse-proxy' };
    const sonarr = { ...radarr, name: 'sonarr' };
    expect(unnamed([radarr, db, self, sonarr], ready).map((t) => t.key)).toEqual(['sonarr']);
  });
});

describe('trustPage', () => {
  const proxyStack = { name: 'reverse-proxy', compose: 'x-fjord:\n  web_port: "8443"\n  web_https: true\n', linkHost: '192.168.4.103', state: { origin: { type: 'catalog' } } };
  const p = { ...ready, trustUrl: 'http://proxy.apps.lan/trust' };
  it('goes by name when this device finds the names', () => {
    expect(trustPage(p, [proxyStack], 'untrusted')).toBe('http://proxy.apps.lan/trust');
  });
  it('goes by address when it cannot', () => {
    expect(trustPage(p, [proxyStack], 'no-dns')).toBe('http://192.168.4.103/trust');
  });
});
