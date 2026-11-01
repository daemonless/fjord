import { describe, it, expect } from 'vitest';
import { restartMeaning, mixedMeaning, restartShort, restartTitle, composeValue, choicesFor, type StackRestart } from './restartPolicy';

const ALL = ['unless-stopped', 'always', 'on-failure', 'no'];
const BOOT = ['unless-stopped', 'always', 'no'];
const r = (policy: string, extra: Partial<StackRestart> = {}): StackRestart => ({ policy, services: {}, policies: ALL, onExit: true, ...extra });
const boot = (policy: string): StackRestart => r(policy, { policies: BOOT, onExit: false });

describe('restartMeaning', () => {
  it('says what happens on exit and at boot', () => {
    expect(restartMeaning('unless-stopped')).toBe('Restarted whenever it exits. Started at boot, unless you stopped it.');
    expect(restartMeaning('always')).toContain('even if you stopped it');
    expect(restartMeaning('no')).toBe('Left down when it exits. Not started at boot.');
  });

  it('names the cap on a crash restart', () => {
    expect(restartMeaning('on-failure', 5)).toContain('up to 5 times');
    expect(restartMeaning('on-failure')).not.toContain('up to');
  });

  it('a stack with no policy says what fjord does with it', () => {
    expect(restartMeaning('')).toContain('started at boot if it was running');
  });

  it('on a boot-only engine says nothing about exits', () => {
    for (const p of [...BOOT, '']) expect(restartMeaning(p, 0, false)).not.toMatch(/exit|Restarted/);
    expect(restartMeaning('no', 0, false)).toBe('Not started at boot: only you start it.');
  });
});

describe('choicesFor', () => {
  it('lists what the engine offers, worded for it', () => {
    expect(choicesFor(ALL, true).map((c) => c.label)).toEqual(['Restart unless stopped', 'Always restart', 'Restart after a crash', "Don't restart"]);
    expect(choicesFor(BOOT, false).map((c) => c.label)).toEqual(['Start at boot', 'Always start at boot', "Don't start at boot"]);
    expect(choicesFor([], true)).toEqual([]);
  });
});

describe('mixedMeaning', () => {
  it('lists each service, sorted, unset ones as not set', () => {
    expect(mixedMeaning({ postgres: 'always', gitea: 'unless-stopped', init: '' })).toBe(
      'Its services differ (gitea: unless-stopped, init: not set, postgres: always). Pick one to set them all.',
    );
  });
});

describe('restartShort', () => {
  it('names the setting in a few words', () => {
    expect(restartShort(r('unless-stopped'))).toBe('Restart unless stopped');
    expect(restartShort(r('always'))).toBe('Always restart');
    expect(restartShort(r('on-failure', { retries: 3 }))).toBe('Restart after a crash (3×)');
    expect(restartShort(r('on-failure'))).toBe('Restart after a crash (5×)');
    expect(restartShort(r('no'))).toBe("Don't restart");
    expect(restartShort(r(''))).toBe('Restart: not set');
  });

  it('on a boot-only engine names the boot choice; unset starts at boot', () => {
    expect(restartShort(boot('always'))).toBe('Always start at boot');
    expect(restartShort(boot('no'))).toBe("Don't start at boot");
    expect(restartShort(boot(''))).toBe('Start at boot');
  });

  it('says when it is mixed or the engine takes none', () => {
    expect(restartShort(r('', { mixed: true }))).toBe('Restart: mixed');
    expect(restartShort(r('unless-stopped', { policies: [] }))).toBe('Restart: not available');
  });
});

describe('restartTitle', () => {
  it('on a boot-only engine says it does not restart', () => {
    expect(restartTitle(boot('unless-stopped'))).toContain('does not restart an app when it stops');
    expect(restartTitle(r('unless-stopped'))).not.toContain('does not restart');
  });
});

describe('composeValue', () => {
  it('spells the choice as the compose does', () => {
    expect(composeValue('unless-stopped')).toBe('unless-stopped');
    expect(composeValue('on-failure', 5)).toBe('on-failure:5');
    expect(composeValue('on-failure')).toBe('on-failure');
    expect(composeValue('no', 5)).toBe('no');
  });
});
