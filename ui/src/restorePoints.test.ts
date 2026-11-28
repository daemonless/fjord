import { describe, it, expect } from 'vitest';
import { notCoveredParts, zfsHintMountpoint } from './restorePoints';

describe('notCoveredParts', () => {
  it('returns null when nothing is excluded', () => {
    expect(notCoveredParts()).toBeNull();
    expect(notCoveredParts([])).toBeNull();
  });

  it('lists up to three paths verbatim', () => {
    expect(notCoveredParts(['/mnt/movies'])).toEqual({ shown: '/mnt/movies', extra: 0 });
    expect(notCoveredParts(['/mnt/a', '/mnt/b', '/mnt/c'])).toEqual({
      shown: '/mnt/a, /mnt/b, /mnt/c',
      extra: 0,
    });
  });

  it('summarizes paths beyond three with extra count', () => {
    expect(notCoveredParts(['/mnt/a', '/mnt/b', '/mnt/c', '/mnt/d', '/mnt/e'])).toEqual({
      shown: '/mnt/a, /mnt/b, /mnt/c',
      extra: 2,
    });
  });
});

describe('zfsHintMountpoint', () => {
  it('extracts mountpoint from zfs create command', () => {
    expect(zfsHintMountpoint('zfs create -o mountpoint=/apps zroot/apps')).toBe('/apps');
    expect(zfsHintMountpoint('zfs create -o mountpoint=/tank/apps tank/apps')).toBe('/tank/apps');
    expect(zfsHintMountpoint('')).toBe('/apps');
  });
});
