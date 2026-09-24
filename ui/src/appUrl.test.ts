import { describe, it, expect, vi } from 'vitest';
import { appUrl } from './appUrl';

describe('appUrl', () => {
  // On its own LAN address a service publishes nothing, so the port comes from
  // the compose -- and only the container side listens there. notes' web
  // linked to 192.168.4.106:8001, its old host port.
  it('uses the container port at a service’s own address', () => {
    const compose = 'services:\n  web:\n    x-fjord-published:\n      - "8001:8000"\n';
    const url = appUrl({
      compose,
      ownAddress: true,
      linkHost: '192.168.4.106',
      status: { containers: [{ address: '192.168.4.106', ports: [] }] },
    });
    expect(url).toBe('http://192.168.4.106:8000');
  });

  it('uses the host port when the link goes to the host', () => {
    vi.stubGlobal('location', { hostname: 'netlab' });
    const compose = 'services:\n  web:\n    ports:\n      - "8001:8000"\n';
    expect(appUrl({ compose, status: { containers: [{ ports: [] }] } })).toBe('http://netlab:8001');
    vi.unstubAllGlobals();
  });
});
