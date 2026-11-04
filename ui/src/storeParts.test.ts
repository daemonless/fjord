import { describe, expect, it } from 'vitest';
import { matchesSearch, partLabels } from './storeParts';

describe('App Store stack parts search and labels', () => {
  const weavestream = {
    id: 'weavestream',
    name: 'Weavestream',
    category: 'Productivity',
    parts: [
      { id: 'weavestream-api', name: 'Weavestream API', image: 'ghcr.io/x/weavestream-api' },
      { id: 'weavestream-worker', name: 'Weavestream Worker', image: 'ghcr.io/x/weavestream-worker' },
      { id: 'weavestream-web', name: 'Weavestream Web', image: 'ghcr.io/x/weavestream-web' },
    ],
  };

  it('finds a stack by any of its part names or ids', () => {
    expect(matchesSearch(weavestream, 'weavestream api')).toBe(true);
    expect(matchesSearch(weavestream, 'weavestream-worker')).toBe(true);
    expect(matchesSearch(weavestream, 'worker')).toBe(true);
    expect(matchesSearch(weavestream, 'radarr')).toBe(false);
  });

  it('strips the stack title prefix from part labels', () => {
    expect(partLabels(weavestream.name, weavestream.parts)).toEqual(['API', 'Worker', 'Web']);
  });
});
