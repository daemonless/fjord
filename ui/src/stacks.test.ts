import { describe, expect, it } from 'vitest';
import {
  behind,
  bucketOf,
  computeFilterCounts,
  computeReorder,
  currentImage,
  existingGroupsOf,
  filterStacks,
  groupStacks,
  isDraftEdited,
  isMultiImage,
  listSnapshot,
  mergeStackList,
  newStackDraft,
  orderStacks,
  primaryContainer,
  reduceStackEvent,
  refTag,
  serviceLabel,
  stackImage,
  stackLabel,
  statusDot,
  statusLabel,
  statusStyle,
  type Stack,
  type UpdateInfo,
} from './stacks';

function makeStack(name: string, overrides: Partial<Stack> = {}): Stack {
  return {
    name,
    dir: `/var/db/fjord/stacks/${name}`,
    compose: `services:\n  app:\n    image: ghcr.io/example/${name}:latest\n`,
    env: 'TZ=UTC\n',
    ...overrides,
  };
}

describe('stacks helpers', () => {
  it('resolves stack labels, images, tags and multi-image detection', () => {
    expect(stackLabel(makeStack('radarr', { displayName: 'Radarr 4K' }))).toBe('Radarr 4K');
    expect(stackLabel(makeStack('radarr'))).toBe('radarr');
    expect(stackLabel(null)).toBe('');

    const s = makeStack('radarr');
    expect(stackImage(s.compose)).toBe('ghcr.io/example/radarr:latest');
    expect(currentImage(s)).toBe('ghcr.io/example/radarr:latest');
    expect(currentImage({ ...s, services: [{ image: 'ghcr.io/example/radarr:5.14' }] })).toBe('ghcr.io/example/radarr:5.14');

    expect(isMultiImage(s.compose)).toBe(false);
    expect(isMultiImage(`services:\n  app:\n    image: a:1\n  db:\n    image: b:2\n`)).toBe(true);

    expect(refTag('ghcr.io/example/radarr:5.14')).toBe('5.14');
    expect(refTag('localhost:5000/radarr')).toBe('latest');
    expect(refTag('ghcr.io/example/radarr:5.14@sha256:1234')).toBe('5.14');
  });

  it('maps status to label, style and dot', () => {
    expect(statusLabel({ state: 'running', containers: [] })).toBe('running');
    expect(statusLabel(undefined)).toBe('checking');
    expect(statusDot({ state: 'running', containers: [] })).toBe('bg-fjord-success');
    expect(statusStyle({ state: 'stopped', containers: [] })).toContain('text-fjord-fg-muted');
  });

  it('creates starter drafts and detects edits', () => {
    const pod = newStackDraft('podman', 'freebsd', 'demo');
    expect(pod.compose).toContain('ghcr.io/daemonless/base:15.1-latest');
    expect(isDraftEdited(pod, 'podman', 'freebsd')).toBe(false);
    expect(isDraftEdited({ ...pod, env: 'TZ=America/New_York\n' }, 'podman', 'freebsd')).toBe(true);

    const lin = newStackDraft('podman', 'linux', 'demo');
    expect(lin.compose).toContain('alpine:3.24');

    const aj = newStackDraft('appjail', 'freebsd', 'demo-stack');
    expect(aj.director).toContain('demo_stack_app');
    expect(isDraftEdited(aj, 'podman', 'freebsd')).toBe(false);
  });

  it('filters, counts, orders and groups stacks', () => {
    const a = makeStack('sonarr', {
      status: { state: 'running', containers: [] },
      state: { group: 'Media', order: 2, engine: 'podman' },
    });
    const b = makeStack('radarr', {
      status: { state: 'stopped', containers: [] },
      state: { group: 'Media', order: 1, engine: 'appjail', desired_state: 'stopped' },
    });
    const c = makeStack(' crashed', {
      name: 'crashed',
      status: { state: 'stopped', containers: [] },
      state: { order: 3, engine: 'podman', desired_state: 'running' },
    });
    const fleet: Record<string, UpdateInfo> = {
      sonarr: { state: 'available' },
      radarr: { state: 'current' },
    };

    expect(behind(fleet.sonarr)).toBe(true);
    expect(behind(fleet.radarr)).toBe(false);

    const counts = computeFilterCounts([a, b, c], fleet);
    expect(counts).toEqual({ running: 1, stopped: 1, problem: 1, updates: 1 });

    expect(filterStacks([a, b, c], 'rad', 'all', fleet).map((s) => s.name)).toEqual(['radarr']);
    expect(orderStacks([a, b, c]).map((s) => s.name)).toEqual(['radarr', 'sonarr', 'crashed']);

    const byGroup = groupStacks(orderStacks([a, b, c]), 'groups');
    expect(byGroup.map(([g, items]) => [g, items.map((i) => i.name)])).toEqual([
      ['Media', ['radarr', 'sonarr']],
      ['', ['crashed']],
    ]);
    expect(existingGroupsOf([a, b, c])).toEqual(['Media']);
    expect(bucketOf(b, 'engine')).toBe('appjail');
  });

  it('computes drag-and-drop reorder positions and group changes', () => {
    const a = makeStack('a', { state: { group: 'G1', order: 1 } });
    const b = makeStack('b', { state: { group: 'G1', order: 2 } });
    const c = makeStack('c', { state: { group: '', order: 3 } });
    const ordered = [a, b, c];

    expect(computeReorder(ordered, ordered, 'a', 'a', false, 'G1')).toBeNull();

    const moved = computeReorder(ordered, ordered, 'c', 'a', false, 'G1');
    expect(moved?.items).toEqual([
      { name: 'c', group: 'G1' },
      { name: 'a', group: 'G1' },
      { name: 'b', group: 'G1' },
    ]);
    expect(moved?.nextStacks.find((s) => s.name === 'c')?.state).toEqual({ group: 'G1', order: 1 });
  });

  it('builds a provisional listOnly snapshot for known stacks and skips unsaved drafts', () => {
    const listed = makeStack('tailscale', {
      env: 'TS_AUTHKEY=\nTZ=UTC\n',
      status: { state: 'partial', containers: [{ name: 'tailscale', state: 'restarting', detail: 'restarted 4x' }] },
    });
    const snap = listSnapshot({ name: 'tailscale', dir: '', compose: '', env: '' }, [listed]);
    expect(snap?.listOnly).toBe(true);
    expect(snap?.dir).toBe('/var/db/fjord/stacks/tailscale');
    expect(snap?.status?.state).toBe('partial');

    const direct = listSnapshot(listed, []);
    expect(direct?.listOnly).toBe(true);
    expect(direct?.name).toBe('tailscale');

    const draft = newStackDraft('podman', 'freebsd', 'draft-1');
    expect(listSnapshot(draft, [listed])).toBeNull();
  });

  it('merges stack list updates without overwriting open editor text', () => {
    const open = makeStack('radarr', { compose: 'edited-compose', env: 'SECRET=1\n', status: { state: 'stopped', containers: [] } });
    const fresh = [makeStack('radarr', { compose: '', env: 'SECRET=\n', status: { state: 'running', containers: [] } })];
    const merged = mergeStackList(open, fresh);
    expect(merged.selectedStack?.compose).toBe('edited-compose');
    expect(merged.selectedStack?.env).toBe('SECRET=1\n');
    expect(merged.selectedStack?.status?.state).toBe('running');
  });

  it('reduces SSE stack events into state transitions and side-effect flags', () => {
    const s = makeStack('radarr', { status: { state: 'running', containers: [] } });
    const other = makeStack('lidarr', { status: { state: 'stopped', containers: [] } });
    const list = [s, other];

    const e1 = reduceStackEvent(list, s, { type: 'stack-busy', name: 'radarr', busy: 'update' });
    expect(e1.stacks[0].busy).toBe('update');
    expect(e1.selectedStack?.busy).toBe('update');
    expect(e1.followOutput).toBe('radarr');

    // Events on another stack must preserve the selectedStack reference so
    // Svelte reactive blocks watching selectedStack (e.g. phone nav drawer)
    // do not fire on unrelated events.
    const eOther = reduceStackEvent(list, s, { type: 'stack', name: 'lidarr', status: { state: 'running', containers: [] } });
    expect(eOther.selectedStack).toBe(s);

    const e2 = reduceStackEvent(e1.stacks, e1.selectedStack, { type: 'stack-busy', name: 'radarr', busy: '' });
    expect(e2.stacks[0].busy).toBeUndefined();
    expect(e2.refreshState).toBe('radarr');
    expect(e2.refreshFleet).toBe(true);
    expect(e2.checkUpdate).toBe('radarr');

    const e3 = reduceStackEvent([s], s, { type: 'stack-removed', name: 'radarr' });
    expect(e3.stacks).toHaveLength(0);
    expect(e3.selectedStack).toBeNull();
    expect(e3.clearSelection).toBe(true);

    const e4 = reduceStackEvent([s], s, { type: 'stack', name: 'sonarr', status: { state: 'running', containers: [] } });
    expect(e4.reloadStacks).toBe(true);
    expect(e4.stacks).toBe(e4.stacks);
    expect(e4.selectedStack).toBe(s);

    const e5 = reduceStackEvent(list, s, { type: 'fleet-updates' });
    expect(e5.refreshFleet).toBe(true);
    expect(e5.stacks).toBe(list);
    expect(e5.selectedStack).toBe(s);
  });

  it('derives short service labels and primary container names', () => {
    expect(serviceLabel('immich', 'immich_server_1')).toBe('server');
    expect(serviceLabel('radarr', 'radarr')).toBe('radarr');

    const compose = `services:\n  server:\n    image: a\n  redis:\n    image: b\n`;
    const containers = [{ name: 'immich_redis_1' }, { name: 'immich_server_1' }];
    expect(primaryContainer(compose, containers)).toBe('immich_server_1');
  });
});
