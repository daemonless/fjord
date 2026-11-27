import { describe, expect, it } from 'vitest';
import { changeLines, historyTitle, imageLabel, whenLabel, type HistoryEntry } from './history';

const e = (p: Partial<HistoryEntry>): HistoryEntry => ({
  at: '2026-11-23T03:00:00Z',
  stack: 'immich',
  action: 'update',
  trigger: 'you',
  result: 'ok',
  ...p,
});

describe('historyTitle', () => {
  it('says what happened, and who when it was not you', () => {
    expect(historyTitle(e({}))).toBe('Updated');
    expect(historyTitle(e({ action: 'restart', trigger: 'recovery' }))).toBe('Restarted by health recovery');
    expect(historyTitle(e({ action: 'start', trigger: 'boot', result: 'failed' }))).toBe('Start failed when the host started');
    expect(historyTitle(e({ action: 'rollback', trigger: 'cli' }))).toBe('Rolled back from the command line');
  });
  it('falls back to the raw action', () => {
    expect(historyTitle(e({ action: 'something-new' }))).toBe('something-new');
  });
});

describe('changeLines', () => {
  it('shows a version change by tag', () => {
    const c = [{ service: 'server', from: { ref: 'ghcr.io/immich-app/immich-server:v1.132.3', digest: 'sha256:aa' }, to: { ref: 'ghcr.io/immich-app/immich-server:v1.133.0', digest: 'sha256:bb' } }];
    expect(changeLines(e({ changes: c }))).toEqual(['server v1.132.3 → v1.133.0']);
  });
  it('shows a rebuild, same tag, by digest', () => {
    const c = [{ service: 'web', from: { ref: 'ghcr.io/daemonless/nginx:latest', digest: 'sha256:1111aaaa2222' }, to: { ref: 'ghcr.io/daemonless/nginx:latest', digest: 'sha256:3333bbbb4444' } }];
    expect(changeLines(e({ changes: c }))).toEqual(['web 1111aaaa → 3333bbbb']);
  });
  it('leaves out a service that did not change or has only one side', () => {
    const same = { ref: 'x:1', digest: 'sha256:aa' };
    expect(changeLines(e({ changes: [{ service: 'db', from: same, to: same }, { service: 'new', to: same }] }))).toEqual([]);
  });
});

describe('imageLabel', () => {
  it('reads a tag past a registry port', () => {
    expect(imageLabel({ ref: 'reg.lan:5000/app' , digest: 'sha256:0123456789abcdef' })).toBe('sha256:01234567');
    expect(imageLabel({ ref: 'reg.lan:5000/app:2.1' })).toBe('2.1');
  });
  it('shortens digest-pinned refs instead of printing the full hash', () => {
    expect(imageLabel({ ref: 'ghcr.io/daemonless/syncthing@sha256:33c8b82470abcdef1234567890' })).toBe('sha256:33c8b824');
    expect(imageLabel({ ref: 'ghcr.io/daemonless/syncthing:latest@sha256:33c8b82470abcdef1234567890' })).toBe('latest');
  });
});

describe('whenLabel', () => {
  it('is the date and time, even a minute ago', () => {
    expect(whenLabel(new Date('2026-11-22T09:30:00').toISOString())).toBe('2026-11-22 09:30');
    expect(whenLabel(new Date(Date.now() - 60000).toISOString())).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/);
  });
});
