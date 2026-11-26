import { describe, it, expect } from 'vitest';
import { isoDate, isoTime, isoDateTime } from './when';

// Local times, so the results do not depend on the test machine's zone.
const t = new Date(2026, 10, 4, 7, 5, 9);

describe('when', () => {
  it('writes dates as YYYY-MM-DD', () => {
    expect(isoDate(t)).toBe('2026-11-04');
  });
  it('writes times 24-hour, seconds when asked', () => {
    expect(isoTime(new Date(2026, 10, 4, 19, 5, 9))).toBe('19:05');
    expect(isoTime(t, true)).toBe('07:05:09');
  });
  it('writes both with a space', () => {
    expect(isoDateTime(t)).toBe('2026-11-04 07:05');
    expect(isoDateTime(t, true)).toBe('2026-11-04 07:05:09');
  });
  it('reads timestamps as strings', () => {
    expect(isoDate('2026-09-22T03:00:00Z', true)).toBe('2026-09-22');
  });
  it('shows nothing for a missing or broken time', () => {
    expect(isoDate('')).toBe('');
    expect(isoDateTime('not a time')).toBe('');
  });
});
