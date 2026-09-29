import { describe, it, expect } from 'vitest';
import { resolveDefault, seedInterfaces, splitPlan, setInterface, serviceType, switchType, isMode, isolatedServices, keepAttachable, addressesNeeded } from './planSeed';

// immich's declaration, as the catalog carries it.
const immich = { 'immich-server': 'default', '*': 'private' };
const services = ['immich-server', 'immich-machine-learning', 'redis', 'database'];
const expand = (plan: Record<string, string>) =>
  Object.fromEntries(services.map((s) => [s, plan[s] ?? plan['*'] ?? '']));

describe('resolveDefault', () => {
  it('picks the configured default when the host has it', () => {
    const out = resolveDefault(expand(immich), [{ name: 'lan' }, { name: 'vlan5' }], 'vlan5');
    expect(out['immich-server']).toBe('vlan5');
  });

  it('falls back to the first real network when the configured one is gone', () => {
    const out = resolveDefault(expand(immich), [{ name: 'lan' }], 'vlan5');
    expect(out['immich-server']).toBe('lan');
  });

  // The bug: the configured default can be a MODE, and a mode is not a
  // network. Taking it left the exposed service on a built-in, which the seed
  // turned into no interfaces at all -- the screen said "no network" with
  // nothing to change.
  it('prefers a real network over a configured mode', () => {
    const out = resolveDefault(expand(immich), [{ name: 'lan' }], 'bridge');
    expect(out['immich-server']).toBe('lan');
  });

  it('leaves everything that is not "default" alone', () => {
    const out = resolveDefault(expand(immich), [{ name: 'lan' }], 'lan');
    expect(out['redis']).toBe('private');
    expect(out['database']).toBe('private');
  });
});

describe('seedInterfaces', () => {
  it('a one-service app on a LAN gets that network only, no private segment', () => {
    expect(seedInterfaces(['zensical'], { zensical: 'lan-dhcp' })).toEqual({
      zensical: [{ network: 'lan-dhcp', ip: '', mac: '' }],
    });
  });
  it('gives the exposed service its network AND the private one', () => {
    const seeded = seedInterfaces(services, resolveDefault(expand(immich), [{ name: 'lan' }], 'lan'));
    expect(seeded['immich-server'].map((r) => r.network)).toEqual(['lan', 'private']);
  });

  it('gives every other service the private one only', () => {
    const seeded = seedInterfaces(services, resolveDefault(expand(immich), [{ name: 'lan' }], 'lan'));
    for (const svc of ['redis', 'database', 'immich-machine-learning']) {
      expect(seeded[svc].map((r) => r.network)).toEqual(['private']);
    }
  });

  // Never an empty list: that rendered as "On no network" with no row to fix.
  it('shows a mode as a row rather than as nothing', () => {
    const seeded = seedInterfaces(services, { ...expand(immich), 'immich-server': 'host' });
    expect(seeded['immich-server']).toEqual([{ network: 'host' }]);
  });

  it('never leaves a service with no rows at all', () => {
    for (const spec of ['lan', 'private', 'host', 'bridge', 'none', '']) {
      const seeded = seedInterfaces(services, { ...expand(immich), 'immich-server': spec });
      expect(seeded['immich-server'].length).toBeGreaterThan(0);
    }
  });
});

describe('splitPlan', () => {
  it('separates interfaces from modes', () => {
    const { networks, modes } = splitPlan({
      'immich-server': [{ network: 'lan', ip: '192.168.4.90' }, { network: 'private' }],
      database: [{ network: 'private' }],
      redis: [{ network: 'host' }],
    });
    expect(networks).toEqual([
      { network: 'lan', service: 'immich-server', ip: '192.168.4.90', ip6: '', mac: '' },
      { network: 'private', service: 'immich-server', ip: '', ip6: '', mac: '' },
      { network: 'private', service: 'database', ip: '', ip6: '', mac: '' },
    ]);
    expect(modes).toEqual({ redis: 'host' });
  });

  it('drops empty rows rather than sending a nameless network', () => {
    const { networks } = splitPlan({ web: [{ network: '' }, { network: 'lan' }] });
    expect(networks).toHaveLength(1);
  });
});

describe('setInterface', () => {
  const rows = [{ network: 'lan', ip: '192.168.4.90' }, { network: 'private' }];

  it('changing a network keeps the other interfaces', () => {
    const { rows: out, replaced } = setInterface(rows, 0, 'network', 'vlan5');
    expect(out.map((r) => r.network)).toEqual(['vlan5', 'private']);
    expect(replaced).toEqual([]);
  });

  it('clears an address that belonged to the old network', () => {
    const { rows: out } = setInterface(rows, 0, 'network', 'vlan5');
    expect(out[0].ip).toBe('');
  });

  // host and none are exclusive -- and used to silently take the private
  // interface with them, which read as the editor losing data.
  it('host replaces the list, and says what it replaced', () => {
    const { rows: out, replaced } = setInterface(rows, 0, 'network', 'host');
    expect(out).toEqual([{ network: 'host' }]);
    expect(replaced.map((r) => r.network)).toEqual(['private']);
  });

  it('a mode on a single-interface service replaces nothing', () => {
    const { replaced } = setInterface([{ network: 'private' }], 0, 'network', 'host');
    expect(replaced).toEqual([]);
  });
});

describe('splitPlan: a service emptied on screen', () => {
  // Omitting it left immich's compose-declared `network_mode: host` in place,
  // so clearing a postgres's interfaces put it on the host's stack instead of
  // on nothing.
  it('says "none" rather than leaving the service out', () => {
    const { networks, modes } = splitPlan({ web: [{ network: 'lan' }], database: [] });
    expect(networks).toHaveLength(1);
    expect(modes).toEqual({ database: 'none' });
  });

  it('treats a row with a blank network the same way', () => {
    const { modes } = splitPlan({ database: [{ network: '' }] });
    expect(modes).toEqual({ database: 'none' });
  });
});

describe('resolveDefault with nothing to join', () => {
  // A host with no attachable network. Resolving the exposed service to a
  // built-in on its own put it on podman's bridge with its database on a
  // private segment: two places that cannot see each other, from a stack that
  // installed without an error.
  it('installs the app the way it ships, whole', () => {
    const out = resolveDefault(expand(immich), [], '', 'host');
    expect(out).toEqual({
      'immich-server': 'host',
      'immich-machine-learning': 'host',
      redis: 'host',
      database: 'host',
    });
  });

  it('keeps a bridge stack on bridge, not on a private segment', () => {
    const out = resolveDefault(expand(immich), [], 'lan', 'bridge');
    expect(new Set(Object.values(out))).toEqual(new Set(['bridge']));
  });

  it('shows one row per service, so the whole stack is still visible', () => {
    const seeded = seedInterfaces(services, resolveDefault(expand(immich), [], '', 'host'));
    for (const svc of services) {
      expect(seeded[svc].map((r) => r.network)).toEqual(['host']);
    }
  });
});

// The screen itself, from the manifest the catalog actually serves. The
// complaint that started this was that immich showed ONE networking control
// for a four-service stack; nothing in the tests below it went near how many
// rows there are, so everything stayed green while the screen stayed wrong.
describe('immich, end to end', () => {
  const manifestNetworking = { 'immich-server': 'default', '*': 'private' };
  const manifestServices = [
    'immich-server',
    'immich-machine-learning',
    'redis',
    'database',
  ];
  const plan = (nets: { name: string }[], configured = '') =>
    seedInterfaces(
      manifestServices,
      resolveDefault(expand(manifestNetworking), nets, configured),
    );

  it('shows a row list for every service, not one control for the stack', () => {
    const seeded = plan([{ name: 'lan' }]);
    expect(Object.keys(seeded).sort()).toEqual([...manifestServices].sort());
    for (const svc of manifestServices) {
      expect(seeded[svc].length).toBeGreaterThan(0);
    }
  });

  it('gives immich-server two interfaces: the network chosen, and the private one', () => {
    const seeded = plan([{ name: 'lan' }], 'lan');
    expect(seeded['immich-server'].map((r) => r.network)).toEqual(['lan', 'private']);
    expect(seeded['database'].map((r) => r.network)).toEqual(['private']);
  });

  it('sends four services to the daemon, database included', () => {
    const { networks, modes } = splitPlan(plan([{ name: 'lan' }], 'lan'));
    expect(new Set(networks.map((n) => n.service))).toEqual(new Set(manifestServices));
    expect(networks.filter((n) => n.service === 'immich-server')).toHaveLength(2);
    expect(modes).toEqual({});
  });

  it('on a host with no network, keeps all four together the way it ships', () => {
    const seeded = seedInterfaces(
      manifestServices,
      resolveDefault(expand(manifestNetworking), [], '', 'host'),
    );
    const { networks, modes } = splitPlan(seeded);
    expect(networks).toHaveLength(0);
    expect(modes).toEqual({
      'immich-server': 'host',
      'immich-machine-learning': 'host',
      redis: 'host',
      database: 'host',
    });
  });
});

describe('resolveDefault and other stacks\' private segments', () => {
  // Every multi-service install leaves one behind. Taking the first network on
  // the host made an orphaned immich_priv the default the NEXT app was
  // offered -- a segment belonging to something else entirely.
  const nets = [
    { name: 'immich_priv', private: true },
    { name: 'lan' },
  ];

  it('never defaults onto one', () => {
    expect(resolveDefault(expand(immich), nets, '')['immich-server']).toBe('lan');
  });

  it('nor onto one the engine allocates', () => {
    const engineNets = [{ name: 'podman', addressSource: 'engine' }, { name: 'lan' }];
    expect(resolveDefault(expand(immich), engineNets, '')['immich-server']).toBe('lan');
  });

  it('when they are all there is, the app installs the way it ships', () => {
    const out = resolveDefault(expand(immich), [{ name: 'immich_priv', private: true }], '', 'host');
    expect(new Set(Object.values(out))).toEqual(new Set(['host']));
  });
});

describe('bridge is an interface, not a mode', () => {
  // host and none clear the other interfaces because podman-compose refuses
  // both keys on one service. bridge is compose's default network and sits
  // next to a LAN fine -- netlab ran bridge + epair with both answering.
  it('picking bridge on one row keeps the others', () => {
    const { rows, replaced } = setInterface([{ network: 'lan' }, { network: 'private' }], 0, 'network', 'bridge');
    expect(rows.map((r) => r.network)).toEqual(['bridge', 'private']);
    expect(replaced).toEqual([]);
  });

  it('bridge alone is still sent as the mode', () => {
    expect(splitPlan({ web: [{ network: 'bridge' }] }).modes).toEqual({ web: 'bridge' });
  });

  it('bridge next to a LAN is sent as an interface', () => {
    const { networks, modes } = splitPlan({ web: [{ network: 'bridge' }, { network: 'lan-dhcp', mac: '0e:04:00:00:00:31' }] });
    expect(modes).toEqual({});
    expect(networks.map((n) => n.network)).toEqual(['bridge', 'lan-dhcp']);
  });

  it('isMode still knows bridge, for seeds and stack-wide choices', () => {
    expect(isMode('bridge')).toBe(true);
  });
});

describe('serviceType / switchType', () => {
  const three = [
    { network: 'lan-dhcp', mac: '58:9c:fc:10:b7:36' },
    { network: 'lan-range', ip: '192.168.86.200' },
    { network: 'lan-static', ip: '192.168.86.49' },
  ];
  it('reads host and none as types, everything else as networks', () => {
    expect(serviceType([{ network: 'host' }])).toBe('host');
    expect(serviceType([{ network: 'none' }])).toBe('none');
    expect(serviceType([{ network: 'bridge' }])).toBe('networks');
    expect(serviceType(three)).toBe('networks');
  });

  // tautulli on army: three networks, switched to host and back, came back
  // empty until a refresh.
  it('host and back gives every interface back unchanged', () => {
    const toHost = switchType(three, 'host', undefined, [{ network: 'bridge' }]);
    expect(toHost.rows).toEqual([{ network: 'host' }]);
    const toNone = switchType(toHost.rows, 'none', toHost.kept, [{ network: 'bridge' }]);
    expect(toNone.rows).toEqual([{ network: 'none' }]);
    const back = switchType(toNone.rows, 'networks', toNone.kept, [{ network: 'bridge' }]);
    expect(back.rows).toEqual(three);
    expect(back.kept).toBeUndefined();
  });

  it('networks with nothing kept starts on bridge', () => {
    expect(switchType([{ network: 'host' }], 'networks', undefined, [{ network: 'bridge' }]).rows).toEqual([{ network: 'bridge' }]);
  });
});

describe('isolatedServices', () => {
  const immichPlan = {
    'immich-server': [{ network: 'lan' }, { network: 'immich_priv' }],
    'immich-machine-learning': [{ network: 'immich_priv' }],
    redis: [{ network: 'immich_priv' }],
    database: [{ network: 'immich_priv' }],
  };

  it('says nothing about a stack whose parts can all reach each other', () => {
    expect(isolatedServices(immichPlan)).toEqual([]);
  });

  // The edit that breaks a working stack silently: postgres keeps running and
  // the server can no longer see it.
  it('catches a service whose last shared interface was removed', () => {
    expect(isolatedServices({ ...immichPlan, database: [] })).toEqual(['database']);
    expect(isolatedServices({ ...immichPlan, database: [{ network: 'lan2' }] })).toEqual(['database']);
  });

  it('catches a service put on a mode the rest of the stack is not on', () => {
    expect(isolatedServices({ ...immichPlan, database: [{ network: 'host' }] })).toEqual(['database']);
  });

  // The whole stack on the host's stack is how these bundles ship: they reach
  // each other over localhost, so nothing is isolated.
  it('treats a shared mode as shared', () => {
    const allHost = Object.fromEntries(Object.keys(immichPlan).map((k) => [k, [{ network: 'host' }]]));
    expect(isolatedServices(allHost)).toEqual([]);
  });

  it('never counts "none" as somewhere two services meet', () => {
    const allNone = Object.fromEntries(Object.keys(immichPlan).map((k) => [k, [{ network: 'none' }]]));
    expect(isolatedServices(allNone).sort()).toEqual(Object.keys(immichPlan).sort());
  });

  // No `networks:` key: compose puts every such service on the project
  // default together (0.2 and hand-written stacks). notes/db read "cut off".
  it('treats rows naming no network as the shared project default', () => {
    expect(isolatedServices({ db: [{ network: '' }], web: [{ network: '' }] })).toEqual([]);
    expect(isolatedServices({ db: [{ network: '' }], web: [{ network: 'bridge' }] })).toEqual([]);
    expect(isolatedServices({ db: [{ network: '' }], web: [{ network: 'lan' }] }).sort()).toEqual(['db', 'web']);
  });

  it('has nothing to say about a one-service stack', () => {
    expect(isolatedServices({ zensical: [] })).toEqual([]);
    expect(isolatedServices({ zensical: [{ network: 'none' }] })).toEqual([]);
  });
});

describe('keepAttachable', () => {
  const fresh = { web: [{ network: 'bridge' }], db: [{ network: 'private' }] };
  // Picked lan, then switched engine: lan exists there too, so it stays.
  it('keeps a pick the new engine can attach', () => {
    const edits = { web: [{ network: 'lan', ip: '192.168.4.9', mac: '' }], db: [{ network: 'private' }] };
    expect(keepAttachable(edits, fresh, [{ name: 'lan' }])).toEqual(edits);
  });
  it('drops only what the new engine cannot attach', () => {
    const edits = { web: [{ network: 'lan' }, { network: 'private' }], db: [{ network: 'private' }] };
    expect(keepAttachable(edits, fresh, [{ name: 'ajnet' }])).toEqual({
      web: [{ network: 'private' }],
      db: [{ network: 'private' }],
    });
  });
  it('gives a service left with nothing its fresh seed', () => {
    expect(keepAttachable({ web: [{ network: 'host' }], db: [] }, fresh, [], ['host'])).toEqual(fresh);
  });
});

describe('addressesNeeded', () => {
  const nets = [
    { name: 'lan', addressSource: 'pool' },
    { name: 'lan-dhcp', addressSource: 'dhcp' },
    { name: 'fixed', addressSource: 'static' },
  ];
  it('appjail on a pool network needs an address typed in', () => {
    expect(addressesNeeded({ web: [{ network: 'lan', ip: '' }] }, nets, 'appjail')).toEqual(['web on lan']);
    expect(addressesNeeded({ web: [{ network: 'lan', ip: '192.168.4.50' }] }, nets, 'appjail')).toEqual([]);
  });
  it('podman draws from the pool; DHCP never needs one; static always does', () => {
    expect(addressesNeeded({ web: [{ network: 'lan' }] }, nets, 'podman')).toEqual([]);
    expect(addressesNeeded({ web: [{ network: 'lan-dhcp' }] }, nets, 'appjail')).toEqual([]);
    expect(addressesNeeded({ db: [{ network: 'fixed' }] }, nets, 'podman')).toEqual(['db on fixed']);
  });
  it('built-in modes and unknown networks are not its business', () => {
    expect(addressesNeeded({ web: [{ network: 'bridge' }, { network: 'host' }] }, nets, 'appjail')).toEqual([]);
  });
});
