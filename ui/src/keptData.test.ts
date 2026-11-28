import { describe, expect, it } from 'vitest';
import { isOlderVersion, keptFor, type Leftover } from './keptData';

describe('isOlderVersion', () => {
  it('is true only for a lower plain version', () => {
    expect(isOlderVersion('2.1.5', '2.1.6')).toBe(true);
    expect(isOlderVersion('2.1', '2.1.6')).toBe(true);
    expect(isOlderVersion('v1.9.0', '2.0')).toBe(true);
    expect(isOlderVersion('2.1.6', '2.1.6')).toBe(false);
    expect(isOlderVersion('2.1.6', '2.1')).toBe(false);
    expect(isOlderVersion('2.2.0', '2.1.9')).toBe(false);
    expect(isOlderVersion('2.10.0', '2.9.0')).toBe(false);
  });
  it('ignores a build suffix', () => {
    expect(isOlderVersion('2.1.6-ls200', '2.1.6-ls123')).toBe(false);
    expect(isOlderVersion('2.1.5_3', '2.1.6')).toBe(true);
  });
  it('does not guess when either side is not a plain version', () => {
    expect(isOlderVersion('latest', '2.1.6')).toBe(false);
    expect(isOlderVersion('2.1.5', '')).toBe(false);
    expect(isOlderVersion('', '2.1.6')).toBe(false);
    expect(isOlderVersion('pkg-latest', 'pkg')).toBe(false);
    expect(isOlderVersion('2026.10.01', '2026.11.01')).toBe(false);
  });
});

describe('keptFor', () => {
  const folders: Leftover[] = [
    { path: '/apps/radarr', bytes: 10 },
    { path: '/apps/radarr.kept-2026-11-27-010203', bytes: 5, setAside: true },
    { path: '/containers/sonarr', bytes: 7 },
  ];
  it('finds the data an install into that folder would use', () => {
    expect(keptFor(folders, '/apps', 'radarr')?.bytes).toBe(10);
    expect(keptFor(folders, '/apps/', 'radarr')?.bytes).toBe(10);
    expect(keptFor(folders, '/containers', 'sonarr')?.bytes).toBe(7);
  });
  it('is nothing for another location, another name, or a set-aside folder', () => {
    expect(keptFor(folders, '/containers', 'radarr')).toBeNull();
    expect(keptFor(folders, '/apps', 'radarr-2')).toBeNull();
    expect(keptFor(folders, '/apps', 'radarr.kept-2026-11-27-010203')).toBeNull();
    expect(keptFor(null, '/apps', 'radarr')).toBeNull();
    expect(keptFor(folders, '', 'radarr')).toBeNull();
  });
});
