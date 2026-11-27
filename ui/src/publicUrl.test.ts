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

describe('a name, and apps that take only a domain', () => {
  it('is the name it is given, wherever it lands', () => {
    expect(suggestedPublicUrl({ ...base, named: 'https://vikunja.apps.lan' })).toBe('https://vikunja.apps.lan');
    expect(suggestedPublicUrl({ ...base, ownAddress: true, ownIP: '192.168.4.50', named: 'https://vikunja.apps.lan' })).toBe('https://vikunja.apps.lan');
  });
  it('is never the host or its own address for an app that needs a domain', () => {
    expect(suggestedPublicUrl({ ...base, needsDomain: true })).toBe('');
    expect(suggestedPublicUrl({ ...base, ownAddress: true, ownIP: '192.168.4.50', needsDomain: true })).toBe('');
    expect(suggestedPublicUrl({ ...base, needsDomain: true, named: 'https://auth.apps.lan' })).toBe('https://auth.apps.lan');
  });
  it('refuses what such an app would refuse at start', () => {
    expect(publicUrlProblem('https://auth.example.com', true)).toBe('');
    expect(publicUrlProblem('https://auth.example.com:8443/', true)).toBe('');
    expect(publicUrlProblem('http://auth.example.com', true)).toBe('Needs https://, not http://');
    expect(publicUrlProblem('https://192.0.2.5:1411', true)).toBe('Needs a domain name, not an address');
    expect(publicUrlProblem('https://[fd00::5]', true)).toBe('Needs a domain name, not an address');
    expect(publicUrlProblem('https://nas:1411', true)).toBe('Needs a full domain name, like nas.example.com');
    expect(publicUrlProblem('auth.example.com', true)).toBe('Start with https://');
    // Any address still does for the rest.
    expect(publicUrlProblem('http://192.0.2.5:3456')).toBe('');
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
