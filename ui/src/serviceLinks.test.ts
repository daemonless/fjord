import { describe, expect, it } from 'vitest';
import { serviceLinks } from './serviceLinks';

describe('serviceLinks', () => {
  it('opens each app on its own LAN address at its own port', () => {
    const compose = `services:
  photocraft:
    image: p
    x-fjord-published:
      - "8080:80"
  vectorcraft:
    image: v
    x-fjord-published:
      - "8081:80"
`;
    const svcs = [
      { name: 'photocraft', address: '192.168.4.106' },
      { name: 'vectorcraft', address: '192.168.4.136' },
    ];
    expect(serviceLinks(compose, {}, svcs, 'netlab')).toEqual([
      { service: 'photocraft', url: 'http://192.168.4.106:80' },
      { service: 'vectorcraft', url: 'http://192.168.4.136:80' },
    ]);
  });

  it('opens a published service at this host, and skips what publishes nothing', () => {
    const compose = `services:
  web:
    image: w
    ports:
      - "\${WEB_PORT}:3456"
      - "5353:53/udp"
  admin:
    image: a
    ports:
      - "127.0.0.1:9000:9000"
  db:
    image: postgres
`;
    expect(serviceLinks(compose, { WEB_PORT: '3460' }, [], 'netlab')).toEqual([
      { service: 'web', url: 'http://netlab:3460' },
      { service: 'admin', url: 'http://netlab:9000' },
    ]);
  });

  it('gives no link to a service on its own address that has none yet', () => {
    const compose = 'services:\n  app:\n    x-fjord-published:\n      - "80:80"\n';
    expect(serviceLinks(compose, {}, [{ name: 'app' }], 'netlab')).toEqual([]);
    expect(serviceLinks('not: [yaml', {}, [], 'h')).toEqual([]);
  });
});
