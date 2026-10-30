import { describe, it, expect } from 'vitest';
import { busyStep } from './busyStep';

// openspeedtest's install on netlab, as fjordd streamed it.
const pull =
  '[fjord] pulling ghcr.io/daemonless/openspeedtest:latest: 100 MB in 24 layers. podman prints nothing while it downloads and writes them; fjord says every 15s that it is still going.\n' +
  '$ podman pull ghcr.io/daemonless/openspeedtest:latest\n' +
  'Trying to pull ghcr.io/daemonless/openspeedtest:latest...\n' +
  'Getting image source signatures\n' +
  'Copying blob sha256:bd9ddc54bea9\n';

describe('busyStep', () => {
  it('names the download and its size', () => {
    expect(busyStep(pull)).toBe('Downloading openspeedtest (100 MB)');
  });

  it('adds how long the download has taken', () => {
    expect(busyStep(pull + '[fjord] still pulling ghcr.io/daemonless/openspeedtest:latest (100 MB) · 30s\n')).toBe(
      'Downloading openspeedtest (100 MB, 30s)',
    );
  });

  it('follows the install through its steps', () => {
    const written = pull + 'Copying config sha256:20d00a\nWriting manifest to image destination\n20d00a08fab8\n';
    expect(busyStep(written)).toBe('Saving the image');
    const up = written + '$ podman-compose --in-pod=false up -d --remove-orphans\nb063cbed0496\n';
    expect(busyStep(up)).toBe('Creating the containers');
    expect(busyStep(up + 't-pr_app_1\n$ podman start t-pr_app_1\n')).toBe('Starting the containers');
  });

  it('counts AppJail\'s jails', () => {
    const out = '$ appjail-director up\n\x1b[1m[2/3] Creating web (myapp_web) ... ';
    expect(busyStep(out)).toBe('Building web (2 of 3)');
    expect(busyStep('$ appjail-director up\n')).toBe('Building the jails');
  });

  it('says nothing for output it does not know', () => {
    expect(busyStep('')).toBe('');
    expect(busyStep('$ podman image prune -f\n')).toBe('');
  });
});
