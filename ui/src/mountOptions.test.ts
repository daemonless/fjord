import { describe, expect, it } from 'vitest';
import { parseOptions, otherOptions, combine, withOption } from './mountOptions';

const shown = [
  { name: 'ro', label: 'Read-only', help: '' },
  { name: 'z', label: 'SELinux: shared', help: '' },
  { name: 'Z', label: 'SELinux: private', help: '' },
];

describe('mount options', () => {
  it('typed options split on commas and spaces, once each', () => {
    expect(parseOptions(' nocopy, noexec  nocopy,')).toEqual(['nocopy', 'noexec']);
    expect(parseOptions('')).toEqual([]);
  });
  it('the typed box holds only what has no checkbox', () => {
    expect(otherOptions(['ro', 'nocopy', 'z'], shown)).toBe('nocopy');
  });
  it('checked ones come in the order offered, then the typed', () => {
    expect(combine(['z', 'ro', 'old'], 'nocopy', shown)).toEqual(['ro', 'z', 'nocopy']);
  });
  it('z and Z replace each other', () => {
    expect(withOption(['ro', 'Z'], 'z')).toEqual(['ro', 'z']);
    expect(withOption(['z'], 'Z')).toEqual(['Z']);
    expect(withOption(['ro'], 'ro')).toEqual(['ro']);
  });
});
