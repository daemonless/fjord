import { describe, expect, it } from 'vitest';
import { defaultPath } from './pathDefaults';

const o = { appId: 'librenms', locations: ['/containers', '/tank/apps'], base: '/containers', slug: 't-lnms', hint: '/containers/t-lnms/config-location' };

describe('defaultPath', () => {
  it("re-roots a default under the app's own folder to this stack's", () => {
    expect(defaultPath('/containers/librenms/config', o)).toBe('/containers/t-lnms/config');
    expect(defaultPath('/containers/librenms/mariadb', o)).toBe('/containers/t-lnms/mariadb');
    expect(defaultPath('/containers/librenms', o)).toBe('/containers/t-lnms');
  });
  it('does so from any configured App data location, into the chosen one', () => {
    expect(defaultPath('/tank/apps/librenms/config', o)).toBe('/containers/t-lnms/config');
    expect(defaultPath('/tank/apps/librenms/config', { ...o, base: '/tank/apps' })).toBe('/tank/apps/t-lnms/config');
  });
  it('is the same folder for the first copy', () => {
    expect(defaultPath('/containers/librenms/config', { ...o, slug: 'librenms' })).toBe('/containers/librenms/config');
  });
  it('leaves other folders as written', () => {
    expect(defaultPath('/mnt/photos', o)).toBe('/mnt/photos');
    expect(defaultPath('/containers/librenms-old/config', o)).toBe('/containers/librenms-old/config');
    expect(defaultPath('{{appdata}}/{{stack}}/config', o)).toBe('{{appdata}}/{{stack}}/config');
  });
  it('falls back to the hint with no default', () => {
    expect(defaultPath('', o)).toBe(o.hint);
  });
});
