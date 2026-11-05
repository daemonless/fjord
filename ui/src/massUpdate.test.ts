import { describe, it, expect } from 'vitest';
import { changesKeyFor, massUpdateRows, pickedMassRows, shortDigest } from './massUpdate';

describe('massUpdateRows', () => {
  it('collects available and upgrade stacks with behind services while skipping busy stacks', () => {
    const stacks = [
      { name: 'radarr', displayName: 'Radarr' },
      { name: 'immich', displayName: 'Immich' },
      { name: 'sonarr', busy: 'update' },
      { name: 'thelounge', displayName: 'The Lounge' },
    ];
    const fleet = {
      radarr: {
        state: 'available',
        services: [{ service: 'radarr', tag: 'latest', state: 'available', running: 'sha256:111111111111999', latest: 'sha256:222222222222999' }],
      },
      immich: {
        state: 'available',
        services: [
          { service: 'immich-server', tag: 'latest', state: 'available', latest: 'sha256:aaaa' },
          { service: 'immich-ml', tag: 'latest', state: 'current' },
          { service: 'redis', tag: '8-pkg', state: 'available', latest: 'sha256:bbbb' },
        ],
      },
      sonarr: {
        state: 'available',
        services: [{ service: 'sonarr', tag: 'latest', state: 'available' }],
      },
      thelounge: {
        state: 'upgrade',
        newTag: '4.6.0-pre.2',
        services: [{ service: 'thelounge', tag: '4.6.0-pre.1', state: 'upgrade', newTag: '4.6.0-pre.2' }],
      },
    };

    const rows = massUpdateRows(stacks, fleet);
    expect(rows).toHaveLength(3);
    expect(rows[0]).toEqual({
      stack: 'radarr',
      label: 'Radarr',
      services: [{ service: 'radarr', tag: 'latest', state: 'available', running: 'sha256:111111111111999', latest: 'sha256:222222222222999' }],
      showServiceName: false,
    });
    expect(rows[1].stack).toBe('immich');
    expect(rows[1].showServiceName).toBe(true);
    expect(rows[1].services.map((s) => s.service)).toEqual(['immich-server', 'redis']);
    expect(rows[2].stack).toBe('thelounge');
    expect(rows[2].services[0].newTag).toBe('4.6.0-pre.2');
  });

  it('filters out unticked stacks and builds stable cache keys', () => {
    const rows = massUpdateRows(
      [{ name: 'radarr' }, { name: 'lidarr' }],
      {
        radarr: { state: 'available', services: [{ service: 'radarr', state: 'available', latest: 'sha256:abcdef0123456789' }] },
        lidarr: { state: 'available', services: [{ service: 'lidarr', state: 'available', latest: 'sha256:9876543210fedcba' }] },
      },
      { radarr: 'pkg', lidarr: 'latest' },
    );
    expect(rows[0].services[0].tag).toBe('pkg');
    expect(shortDigest(rows[0].services[0].latest)).toBe('abcdef012345');
    expect(changesKeyFor('radarr', rows[0].services[0])).toBe('radarr:radarr:sha256:abcdef0123456789');
    expect(pickedMassRows(rows, { radarr: true }).map((r) => r.stack)).toEqual(['lidarr']);
  });
});
