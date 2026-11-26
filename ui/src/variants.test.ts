import { describe, expect, it } from 'vitest';
import { flatChoices, releaseLine, variantChoices, type Pin } from './variants';

const pins = (...vs: string[]): Pin[] => vs.map((v) => ({ version: v, tag: v }));

describe('releaseLine', () => {
  it('reads the line a tag starts with', () => {
    expect(releaseLine('15-latest')).toBe('15');
    expect(releaseLine('3.4-lua-pkg-latest')).toBe('3.4');
    expect(releaseLine('313t-pkg-latest')).toBe('313t');
    expect(releaseLine('17')).toBe('17');
    expect(releaseLine('pkg-latest')).toBe('');
    expect(releaseLine('devel-pkg-latest')).toBe('');
  });
});

describe('variantChoices', () => {
  // Forgejo: three lines, several builds each; 15-latest is the default.
  const forgejo = [
    { id: '15', label: 'v15 branch.' },
    { id: '15-latest', label: 'v15 branch, latest packages.', default: true },
    { id: '15-pkg-latest' },
    { id: '16' },
    { id: '17' },
    { id: '17-latest' },
  ];
  const forgejoTrains = {
    '15': pins('15.0.12'),
    '15-latest': pins('15.0.11'),
    '15-pkg-latest': pins('15.0.11_1'),
    '16': pins('16.0.7'),
    '17': pins('17.0.2'),
    '17-latest': pins('17.0.1'),
    // Published but not a variant any more: never offered.
    '11': pins('11.0.13'),
    'lts': pins('15.0.12'),
    '14-pkg': pins('14.0.5'),
  };

  it('groups by release line, newest line first, recommended first in its group', () => {
    const g = variantChoices(forgejo, forgejoTrains);
    expect(g.map((x) => x.line)).toEqual(['17', '16', '15']);
    expect(g[2].choices.map((c) => c.id)).toEqual(['15-latest', '15', '15-pkg-latest']);
  });

  it('offers only catalog variants, never leftover tags or aliases', () => {
    const ids = flatChoices(variantChoices(forgejo, forgejoTrains)).map((c) => c.id);
    expect(ids).not.toContain('11');
    expect(ids).not.toContain('lts');
    expect(ids).not.toContain('14-pkg');
    expect(ids).toHaveLength(6);
  });

  it('names each variant by tag and its own newest release', () => {
    const c = flatChoices(variantChoices(forgejo, forgejoTrains));
    expect(c.find((x) => x.id === '15-latest')!.text).toBe('15-latest · 15.0.11  (recommended)');
    expect(c.find((x) => x.id === '15')!.text).toBe('15 · 15.0.12');
    expect(c.find((x) => x.id === '15')!.desc).toBe('v15 branch.');
    expect(c.find((x) => x.id === '15-pkg-latest')!.desc).toBe('');
  });

  it('keeps one list when each line has a single build, newest first', () => {
    const redis = ['8.8-pkg-latest', '6.2-pkg-latest', '7.4-pkg-latest', '8.0-pkg-latest'].map((id) => ({ id, default: id === '8.8-pkg-latest' }));
    const trains = Object.fromEntries(redis.map((v) => [v.id, pins('x')]));
    const g = variantChoices(redis, trains);
    expect(g).toHaveLength(1);
    expect(g[0].choices.map((c) => c.id)).toEqual(['8.8-pkg-latest', '8.0-pkg-latest', '7.4-pkg-latest', '6.2-pkg-latest']);
  });

  it('keeps an unnumbered image in its own order, recommended first', () => {
    const v = [{ id: 'latest' }, { id: 'pkg' }, { id: 'pkg-latest', default: true }];
    const g = variantChoices(v, {});
    expect(g[0].choices.map((c) => c.id)).toEqual(['pkg-latest', 'latest', 'pkg']);
  });

  it('shows every catalog variant before the registry answers', () => {
    expect(flatChoices(variantChoices(forgejo, {}))).toHaveLength(6);
  });

  it('falls back to the registry channels when the catalog has no variants', () => {
    const ids = flatChoices(variantChoices([], { pkg: pins('1.0'), latest: pins('1.1') })).map((c) => c.id);
    expect(ids.sort()).toEqual(['latest', 'pkg']);
  });

  it('puts an unnumbered variant after the numbered lines (haproxy devel)', () => {
    const v = ['3.4-pkg-latest', '3.4-lua-pkg-latest', '3.3-pkg-latest', 'devel-pkg-latest'].map((id) => ({ id }));
    const g = variantChoices(v, {});
    expect(g.map((x) => x.line)).toEqual(['3.4', '3.3', '']);
  });
});
