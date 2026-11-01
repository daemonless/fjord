import { describe, it, expect } from 'vitest';
import { sizeWords, humanBytes } from './installSize';

describe('humanBytes', () => {
  it('counts like the registry', () => {
    expect(humanBytes(1_234_000_000)).toBe('1.2 GB');
    expect(humanBytes(87_400_000)).toBe('87 MB');
    expect(humanBytes(999)).toBe('999 B');
  });
});

describe('sizeWords', () => {
  const img = (bytes: number, extra = {}) => ({ image: 'x', bytes, ...extra });
  it('one image: just the size', () => {
    expect(sizeWords({ images: [img(87_400_000)], download: 87_400_000 })).toBe('87 MB');
  });
  it('several, some already here', () => {
    expect(sizeWords({ images: [img(1e9), img(2e8, { local: true }), img(3e8)], download: 1.3e9 })).toBe(
      '1.3 GB (2 images, 1 already here)',
    );
  });
  it('all on the host: still the size', () => {
    expect(sizeWords({ images: [img(98_000_000, { local: true })], download: 0 })).toBe('none, image already on this host (98 MB)');
    expect(sizeWords({ images: [img(1e9, { local: true }), img(2e8, { local: true })], download: 0 })).toBe(
      'none, 2 images already on this host (1.2 GB)',
    );
    expect(sizeWords({ images: [img(0, { local: true, unknown: true })], download: 0 })).toBe('none, image already on this host');
  });
  it('says when part of it is unknown, and nothing when all is', () => {
    expect(sizeWords({ images: [img(5e8), img(0, { unknown: true })], download: 5e8 })).toBe(
      '500 MB (2 images, 1 of unknown size)',
    );
    expect(sizeWords({ images: [img(0, { unknown: true })], download: 0 })).toBe('');
    expect(sizeWords(null)).toBe('');
  });
});
