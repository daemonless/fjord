import { describe, expect, it } from 'vitest';
import { publicUrlProblem, suggestedPublicUrl } from './publicUrl';

const base = { webPort: '${WEB_PORT}', https: false, values: { WEB_PORT: '3456' }, host: 'netlab', ownAddress: false, ownIP: '', insidePort: '3456' };

describe('suggestedPublicUrl', () => {
  it('is this host and the published port', () => {
    expect(suggestedPublicUrl(base)).toBe('http://netlab:3456');
    expect(suggestedPublicUrl({ ...base, values: { WEB_PORT: '3460' } })).toBe('http://netlab:3460');
    expect(suggestedPublicUrl({ ...base, webPort: '8443', https: true })).toBe('https://netlab:8443');
  });
  it('is the app’s own address and port on a LAN network', () => {
    expect(suggestedPublicUrl({ ...base, ownAddress: true, ownIP: '192.168.4.50', insidePort: '3456', values: { WEB_PORT: '9999' } })).toBe(
      'http://192.168.4.50:3456',
    );
  });
  it('is unknown until DHCP hands out the address, or with no port', () => {
    expect(suggestedPublicUrl({ ...base, ownAddress: true })).toBe('');
    expect(suggestedPublicUrl({ ...base, values: {} })).toBe('');
  });
});

describe('publicUrlProblem', () => {
  it('wants an http(s) address with a host', () => {
    expect(publicUrlProblem('f/')).toBe('Start with http:// or https://');
    expect(publicUrlProblem('http://')).not.toBe('');
    expect(publicUrlProblem('https://tasks.example.com/')).toBe('');
    expect(publicUrlProblem('')).toBe('');
  });
});
