import { describe, expect, it } from 'vitest';
import { iconsByImage, serviceIcon } from './appIcons';

// crafting-apps' ten services each showed the same placeholder: every one is
// an app in the catalog, found by its image.
describe('service icons', () => {
  const byImage = iconsByImage([
    { id: 'crafting-apps', image: 'ghcr.io/daemonless/photocraft', icon: 'icons/crafting-apps.svg' },
    { id: 'photocraft', image: 'ghcr.io/daemonless/photocraft', icon: 'icons/photocraft.png' },
    { id: 'wordcraft', image: 'ghcr.io/daemonless/wordcraft', icon: 'icons/wordcraft.png' },
    { id: 'noicon', image: 'ghcr.io/daemonless/noicon' },
  ]);
  it('finds the app by the image, tag and digest aside', () => {
    expect(serviceIcon(byImage, 'ghcr.io/daemonless/wordcraft:latest')).toBe('icons/wordcraft.png');
    expect(serviceIcon(byImage, 'ghcr.io/daemonless/wordcraft:pkg@sha256:abc')).toBe('icons/wordcraft.png');
  });
  it('prefers the app named like the image over a stack that names it', () => {
    expect(serviceIcon(byImage, 'ghcr.io/daemonless/photocraft:latest')).toBe('icons/photocraft.png');
  });
  it('has nothing for an image no catalog knows', () => {
    expect(serviceIcon(byImage, 'ghcr.io/daemonless/mariadb:11.4')).toBe('');
    expect(serviceIcon(byImage, 'ghcr.io/daemonless/noicon:latest')).toBe('');
    expect(serviceIcon(byImage, undefined)).toBe('');
  });
});
