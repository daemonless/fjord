import { describe, expect, it } from 'vitest';
import { addressProblem, usableRange } from './network.ts';

// The networks fjord actually defines, in the shapes that have caused bugs.
const staticPod = { subnet: '192.168.4.0/24', gateway: '192.168.4.1', addressSource: 'static' };
const pool = { subnet: '10.78.0.0/24', gateway: '10.78.0.1', addressSource: 'pool' };
// appjail's own: it hands out 10.0.0.1 -- the address it also calls the
// gateway -- as the first in the range, so the gateway rule must not apply.
const engineOwned = { subnet: '10.0.0.0/10', gateway: '10.0.0.1', addressSource: 'engine' };

describe('addressProblem', () => {
  it('accepts an ordinary host address', () => {
    expect(addressProblem('192.168.4.50', staticPod)).toBe('');
    expect(addressProblem('10.78.0.9', pool)).toBe('');
  });

  it('refuses the three addresses that are not hosts', () => {
    expect(addressProblem('192.168.4.0', staticPod)).toContain('is the network address');
    expect(addressProblem('192.168.4.255', staticPod)).toContain('is the broadcast address');
    expect(addressProblem('192.168.4.1', staticPod)).toContain('is the gateway');
  });

  it('refuses an address on another segment', () => {
    expect(addressProblem('10.1.1.1', staticPod)).toBe('10.1.1.1 is not in 192.168.4.0/24');
  });

  // "1" reached a jail as `ifconfig sb_x:1/24` -> 0.0.0.1: configured-looking
  // and reaching nothing. ipaddr.js's isValid() accepts it; the strict form
  // does not, which is why this test names the inputs rather than the API.
  it('refuses things that are not dotted quads', () => {
    for (const bad of ['1', '1.2.3.4.5', '010.1.1.1', '192.168.4.256', '0x0a.1.1.1', 'banana']) {
      expect(addressProblem(bad, staticPod), bad).toContain('is not an IPv4 address');
    }
  });

  it('says nothing about an empty field or an unknown segment', () => {
    expect(addressProblem('', staticPod)).toBe('');
    expect(addressProblem('10.9.9.9', { subnet: '' })).toBe('');
    expect(addressProblem('10.9.9.9', undefined)).toBe('');
  });

  // The engine allocates here, and its gateway is allocatable. Everything else
  // still holds: appjail's own range stops short of network and broadcast.
  it('leaves the gateway alone on an engine-allocated network', () => {
    expect(addressProblem('10.0.0.1', engineOwned)).toBe('');
    expect(addressProblem('10.0.0.0', engineOwned)).toContain('is the network address');
    expect(addressProblem('10.63.255.255', engineOwned)).toContain('is the broadcast address');
    expect(addressProblem('192.168.86.1', engineOwned)).toBe('192.168.86.1 is not in 10.0.0.0/10');
  });

  // A /32 is one host; its network and broadcast addresses ARE that host. The
  // daemon guards on prefix length the same way.
  it('does not call a /32 its own network address', () => {
    expect(addressProblem('10.9.0.5', { subnet: '10.9.0.5/32' })).toBe('');
  });
});

describe('usableRange', () => {
  it('trims the gateway off whichever end it sits on', () => {
    expect(usableRange(staticPod)).toBe('192.168.4.2 – 192.168.4.254');
    expect(usableRange({ subnet: '10.1.0.0/24', gateway: '10.1.0.254' })).toBe('10.1.0.1 – 10.1.0.253');
  });

  // Matches appjail's own MINADDR/MAXADDR for this network.
  it('matches what the engine itself would hand out', () => {
    expect(usableRange(engineOwned)).toBe('10.0.0.1 – 10.63.255.254');
  });

  it('says nothing when there is no host range to state', () => {
    expect(usableRange({ subnet: '10.9.0.0/31' })).toBe('');
    expect(usableRange({ subnet: '10.9.0.5/32' })).toBe('');
    expect(usableRange({ subnet: '' })).toBe('');
    expect(usableRange(undefined)).toBe('');
  });
});
