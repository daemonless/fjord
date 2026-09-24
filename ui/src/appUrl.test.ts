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

  // clamav and mariadb have no web UI, and the catalog says so by leaving
  // out web_port. The first published port is a TCP socket, not a page.
  it('offers no link for a catalog app without a web port', () => {
    const compose = 'services:\n  clamav:\n    ports:\n      - "3310:3310"\n';
    expect(appUrl({ compose, state: { origin: { type: 'catalog' } }, status: { containers: [{ ports: [{ hostPort: 3310, containerPort: 3310 }] }] } })).toBe('');
  });

  it('still links a catalog app that has one', () => {
    const compose = 'services:\n  tautulli:\n    ports:\n      - "8181:8181"\n\nx-fjord:\n  web_port: "8181"\n';
    vi.stubGlobal('location', { hostname: 'netlab' });
    expect(appUrl({ compose, state: { origin: { type: 'catalog' } } })).toBe('http://netlab:8181');
    vi.unstubAllGlobals();
  });
});
