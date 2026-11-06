import { describe, expect, it } from 'vitest';
import { loadSnoozed, SNOOZE_MS, visibleConditions, type Condition } from './conditions';

const cond = (id: string, severity: Condition['severity'] = 'warning'): Condition => ({
  id,
  severity,
  scope: 'host',
  title: id,
});

describe('visibleConditions', () => {
  it('orders critical conditions before warnings and respects 24h snooze for warnings only', () => {
    const now = 1_700_000_000_000;
    const snoozed = {
      'reboot-needed': now + SNOOZE_MS,
      'socket': now + SNOOZE_MS, // critical ignores snooze
      'podman-stale': now - 1000, // expired snooze
    };
    const list = [
      cond('reboot-needed', 'warning'),
      cond('podman-stale', 'warning'),
      cond('socket', 'critical'),
    ];
    expect(visibleConditions(list, snoozed, now).map((c) => c.id)).toEqual(['socket', 'podman-stale']);
  });
});

describe('loadSnoozed', () => {
  it('drops expired entries and handles invalid JSON', () => {
    const now = 1000;
    expect(loadSnoozed(null, now)).toEqual({});
    expect(loadSnoozed('{bad', now)).toEqual({});
    expect(loadSnoozed(JSON.stringify({ old: 500, active: 2000 }), now)).toEqual({ active: 2000 });
  });
});
