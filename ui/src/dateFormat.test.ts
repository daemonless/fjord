import { describe, it, expect, beforeEach } from 'vitest';
import { FORMATS, DEFAULT_FORMAT, currentDateFormat, setDateFormat, formatById, formatDate } from './dateFormat';

const store = new Map<string, string>();
beforeEach(() => {
  store.clear();
  globalThis.localStorage = {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  } as Storage;
});

describe('date formats', () => {
  it('lists every strictly ISO 8601 format once, then the others', () => {
    const iso = FORMATS.filter((f) => f.iso);
    expect(iso.length).toBe(124);
    expect(new Set(FORMATS.map((f) => f.id)).size).toBe(FORMATS.length);
    expect(FORMATS.slice(124).map((f) => f.id)).toEqual(['month-first', 'day-first', 'written-out']);
  });
  it('can write every format it lists', () => {
    for (const f of FORMATS) {
      if (f.format === 'YYYY-MM-DD') continue;
      expect(formatDate(2026, 7, 7, f.format), f.id).not.toBe('2026-07-07');
    }
  });
  it('starts on the extended calendar date', () => {
    expect(currentDateFormat()).toBe(DEFAULT_FORMAT);
    expect(formatById(DEFAULT_FORMAT).format).toBe('YYYY-MM-DD');
  });
  it('remembers a choice, and forgets it on the default', () => {
    setDateFormat('iso-8601-extended-week');
    expect(currentDateFormat()).toBe('iso-8601-extended-week');
    setDateFormat('written-out');
    expect(currentDateFormat()).toBe('written-out');
    setDateFormat(DEFAULT_FORMAT);
    expect(store.size).toBe(0);
  });
  it('ignores a format it does not know', () => {
    setDateFormat('yyyy-dd-mm');
    expect(currentDateFormat()).toBe(DEFAULT_FORMAT);
    store.set('fjord-date-format', 'yyyy-dd-mm');
    expect(currentDateFormat()).toBe(DEFAULT_FORMAT);
  });
});

describe('formatDate', () => {
  it('writes the ISO 8601 calendar and ordinal dates', () => {
    expect(formatDate(2026, 7, 7, 'YYYY-MM-DD')).toBe('2026-07-07');
    expect(formatDate(2026, 7, 7, 'YYYYMMDD')).toBe('20260707');
    expect(formatDate(2026, 7, 7, 'YYYY-DDD')).toBe('2026-188');
    expect(formatDate(2024, 12, 31, 'YYYYDDD')).toBe('2024366');
  });
  it('writes ISO week dates, in the week-numbering year', () => {
    expect(formatDate(2026, 7, 7, 'YYYY-Www-D')).toBe('2026-W28-2');
    expect(formatDate(2026, 11, 27, 'YYYYWwwD')).toBe('2026W485');
    // 2027-01-01 is a Friday: still week 53 of 2026.
    expect(formatDate(2027, 1, 1, 'YYYY-Www-D')).toBe('2026-W53-5');
    // 2024-12-30 is a Monday: week 1 of 2025.
    expect(formatDate(2024, 12, 30, 'YYYY-Www-D')).toBe('2025-W01-1');
  });
  it('writes the others', () => {
    expect(formatDate(2026, 7, 7, 'MM/DD/YYYY')).toBe('07/07/2026');
    expect(formatDate(2026, 7, 7, 'DD/MM/YYYY')).toBe('07/07/2026');
    expect(formatDate(2026, 11, 2, 'DD/MM/YYYY')).toBe('02/11/2026');
    expect(formatDate(2026, 7, 7, 'MMMM Do, YYYY')).toBe('July 7th, 2026');
    expect(formatDate(2026, 7, 1, 'MMMM Do, YYYY')).toBe('July 1st, 2026');
    expect(formatDate(2026, 7, 22, 'MMMM Do, YYYY')).toBe('July 22nd, 2026');
    expect(formatDate(2026, 7, 13, 'MMMM Do, YYYY')).toBe('July 13th, 2026');
    expect(formatDate(2026, 7, 7, 'MMMM Do, YYYY', 'fr')).toBe('7 juillet 2026');
    expect(formatDate(2026, 7, 1, 'MMMM Do, YYYY', 'fr')).toBe('1er juillet 2026');
  });
});
