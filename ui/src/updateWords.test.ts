import { describe, it, expect } from 'vitest';
import { changeSentence, packagesPhrase } from './updateWords';

describe('changeSentence', () => {
  it('a rebuild says the version stayed and when it was built', () => {
    expect(
      changeSentence({
        class: 'rebuild',
        versionFrom: '10.11.19',
        versionTo: '10.11.19',
        createdFrom: '2026-09-21T03:00:00Z',
        createdTo: '2026-09-22T03:00:00Z',
        packages: true,
      }),
    ).toBe('New build of the same version (10.11.19), built Sep 22 — no package changes');
  });

  it('a version bump names both versions and the packages', () => {
    expect(changeSentence({ class: 'patch', versionFrom: '0.0.63', versionTo: '0.0.64', packages: true, changed: [1] })).toBe(
      'New patch version, 0.0.63 → 0.0.64 — 1 package changed',
    );
  });

  it('a major version asks for the release notes', () => {
    expect(changeSentence({ class: 'major', versionFrom: '1.4', versionTo: '2.0', packages: false })).toBe(
      'New major version, 1.4 → 2.0 — read its release notes first',
    );
  });

  // No SBOM, no version labels: the image moved and nothing more is known.
  it('says little when little is known', () => {
    expect(changeSentence({ class: 'unknown', packages: false })).toBe('Newer image');
  });
});

describe('packagesPhrase', () => {
  it('counts in words', () => {
    expect(packagesPhrase({ packages: true, changed: [1, 2, 3], added: [1] })).toBe('3 packages changed, 1 added');
    expect(packagesPhrase({ packages: true, removed: [1] })).toBe('1 package removed');
    expect(packagesPhrase({ packages: false })).toBe('');
  });
});
