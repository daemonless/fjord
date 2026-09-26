import { describe, expect, it } from 'vitest';
import { plan, purpose, outcome, type Check } from './setupPlan';

const ENGINES = ['podman', 'appjail'];
const c = (id: string, over: Partial<Check> = {}): Check => ({ id, name: id, status: 'fail', ...over });

// A fresh host, as the doctor reports it for podman.
const fresh: Check[] = [
  c('podman', { engine: 'podman', installable: true }),
  c('socket', { engine: 'podman', installable: true }),
  c('compose', { engine: 'podman', installable: true }),
  c('podman-stale', { engine: 'podman', status: 'ok' }),
  c('epair', { engine: 'podman', group: 'networking', optional: true, status: 'warn', installable: true }),
  c('pf', { engine: 'podman', group: 'firewall', status: 'warn' }),
  c('appjail', { status: 'unknown', installable: true }),
  c('root', { status: 'ok' }),
];

describe('setup plan', () => {
  it('puts the engine first and leaves the other engine out', () => {
    const p = plan([...fresh].reverse(), 'podman', ENGINES);
    expect(p.auto.map((x) => x.id)[0]).toBe('podman');
    expect(p.auto.map((x) => x.id)).not.toContain('appjail');
    expect(p.auto.map((x) => x.id).sort()).toEqual(['compose', 'epair', 'podman', 'socket']);
  });

  it('lists only what needs a person as manual', () => {
    const p = plan(fresh, 'podman', ENGINES);
    expect(p.manual.map((x) => x.id)).toEqual(['pf']);
    expect(p.ready).toBe(false);
  });

  it('is ready once only optional things are left', () => {
    const done = fresh.map((x) => (x.optional ? x : { ...x, status: 'ok' as const, installable: false }));
    const p = plan(done, 'podman', ENGINES);
    expect(p.ready).toBe(true);
    expect(p.auto.map((x) => x.id)).toEqual(['epair']);
  });

  it('an optional check fjord cannot install is an extra, not a problem', () => {
    const p = plan([c('appjail-git', { engine: 'appjail', optional: true, status: 'warn' })], 'appjail', ENGINES);
    expect(p.extras.map((x) => x.id)).toEqual(['appjail-git']);
    expect(p.ready).toBe(true);
  });

  it('the appjail engine check belongs to the appjail plan', () => {
    const p = plan(fresh, 'appjail', ENGINES);
    expect(p.auto.map((x) => x.id)).toEqual(['appjail']);
  });
});

describe('both engines', () => {
  it('sets both up, each engine before its own steps, shared checks once', () => {
    const checks = [...fresh, c('appjail-dns', { engine: 'appjail', group: 'networking', installable: true })];
    const p = plan(checks, ['podman', 'appjail'], ENGINES);
    const ids = p.auto.map((x) => x.id);
    expect(ids[0]).toBe('podman');
    expect(ids.indexOf('appjail')).toBeLessThan(ids.indexOf('appjail-dns'));
    expect(ids).toContain('socket');
    expect(p.ok.filter((x) => x.id === 'root')).toHaveLength(1);
  });

  it('nothing chosen means nothing to do', () => {
    const p = plan(fresh, [], ENGINES);
    expect(p.auto).toEqual([]);
    expect(p.manual).toEqual([]);
  });
});

describe('purpose', () => {
  it('is the first sentence of why', () => {
    expect(purpose(c('x', { why: 'Runs the containers. Every stack on the podman engine is created through it.' }))).toBe('Runs the containers');
  });
});

describe('install stream outcome', () => {
  it('reads the last line', () => {
    expect(outcome('$ pkg install -y podman\nInstalled\n[done]\n')).toBeNull();
    expect(outcome('$ pkg install -y x\n[error] pkg install -y x: exit status 1\n')).toBe('pkg install -y x: exit status 1');
  });
  it('a stream cut short is a failure, not a success', () => {
    expect(outcome('$ pkg install -y podman\nFetching')).toMatch(/ended before/);
  });
});

describe('step order', () => {
  it('one engine at a time: engine, packages, downloads, services; optional last', () => {
    const checks: Check[] = [
      c('podman', { engine: 'podman', installable: true, commands: ['pkg install -y podman'] }),
      c('socket', { engine: 'podman', installable: true, commands: ['sysrc podman_service_enable=YES', 'service podman_service onerestart'] }),
      c('compose', { engine: 'podman', installable: true, commands: ['pkg install -y sysutils/podman-compose'] }),
      c('epair', { engine: 'podman', optional: true, installable: true, commands: ['fetch -o x y'] }),
      c('dnsname', { engine: 'podman', installable: true, commands: ['pkg install -y cni-dnsname'] }),
      c('appjail-dns', { engine: 'appjail', installable: true, commands: ['service appjail-dns onerestart'] }),
      c('appjail', { status: 'unknown', installable: true, commands: ['pkg install -y appjail sysutils/py-director'] }),
    ];
    const p = plan(checks, ['podman', 'appjail'], ENGINES);
    expect(p.auto.map((x) => x.id)).toEqual(['podman', 'compose', 'dnsname', 'socket', 'epair', 'appjail', 'appjail-dns']);
  });
});

describe('all checks', () => {
  it('lists every check for the chosen engine, ok ones too, in setup order', () => {
    const p = plan(fresh, 'podman', ENGINES);
    const ids = p.all.map((x) => x.id);
    expect(ids[0]).toBe('podman');
    expect(ids).toContain('podman-stale');
    expect(ids).toContain('root');
    expect(ids).not.toContain('appjail');
  });
});
