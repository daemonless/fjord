import { describe, expect, it } from 'vitest';
import { forwardDeploy } from './installForward';

describe('App Store forwards the install wizard', () => {
  it('keeps the per-service network plan', () => {
    const detail = {
      name: 'home-assistant',
      engine: 'podman',
      network: '',
      networks: [{ network: 'lan-dhcp', service: 'home-assistant', ip: '', ip6: '', mac: '' }],
      networkModes: { db: 'none' },
      networkPlan: { web: 'default' },
    };
    const out = forwardDeploy(detail, 'home-assistant');
    expect(out.networks).toEqual(detail.networks);
    expect(out.networkModes).toEqual(detail.networkModes);
    expect(out.networkPlan).toEqual(detail.networkPlan);
    expect(out.appId).toBe('home-assistant');
  });

  it('forwards every field, and names an unnamed install after the app', () => {
    const detail = { name: '', a: 1, b: 'two', c: [3] };
    const out = forwardDeploy(detail, 'pkg-cache');
    expect(out).toEqual({ ...detail, name: 'pkg-cache', appId: 'pkg-cache' });
  });
});
