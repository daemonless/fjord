import { describe, it, expect } from 'vitest';
import { activeNotices, groupNoticesByKind, hasActiveNotices, type FleetNotice } from './failureNotices';

describe('failureNotices helpers', () => {
  it('filters out dismissed notices for app banners and sidebar marks', () => {
    const notices = [
      { kind: 'daily-restore-point' as const, count: 3, firstAt: '2026-11-21T02:00:00Z', lastAt: '2026-11-23T02:00:00Z', dismissed: true },
      { kind: 'auto-update' as const, count: 2, firstAt: '2026-11-22T03:00:00Z', lastAt: '2026-11-23T03:00:00Z' },
    ];
    expect(activeNotices(notices)).toHaveLength(1);
    expect(activeNotices(notices)[0].kind).toBe('auto-update');
    expect(hasActiveNotices(notices)).toBe(true);
    expect(hasActiveNotices([notices[0]])).toBe(false);
    expect(hasActiveNotices(undefined)).toBe(false);
  });

  it('groups fleet notices by kind and keeps dismissed notices in Settings summary', () => {
    const notices: FleetNotice[] = [
      { stack: 'sonarr', kind: 'auto-update-no-snapshot', count: 2, firstAt: '2026-11-22T03:00:00Z', lastAt: '2026-11-23T03:00:00Z' },
      { stack: 'radarr', kind: 'daily-restore-point', count: 6, firstAt: '2026-11-21T02:00:00Z', lastAt: '2026-11-26T02:00:00Z', dismissed: true },
      { stack: 'lidarr', kind: 'daily-restore-point', count: 1, firstAt: '2026-11-26T02:00:00Z', lastAt: '2026-11-26T02:00:00Z' },
    ];
    const groups = groupNoticesByKind(notices);
    expect(groups.map((g) => g.kind)).toEqual(['daily-restore-point', 'auto-update-no-snapshot']);
    expect(groups[0].items.map((i) => i.stack)).toEqual(['radarr', 'lidarr']);
    expect(groups[0].items[0].dismissed).toBe(true);
  });
});
