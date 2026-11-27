import { describe, expect, it } from 'vitest';
import { keepsUpToDate, resultLine, runSummary } from './autoupdate';

describe('automatic updates', () => {
  it('is on for any policy that applies updates by itself', () => {
    expect(keepsUpToDate('minor')).toBe(true);
    expect(keepsUpToDate('rebuilds')).toBe(true);
    expect(keepsUpToDate('notify')).toBe(false);
    expect(keepsUpToDate('off')).toBe(false);
    expect(keepsUpToDate(undefined)).toBe(false);
  });
  it('says what happened to each stack', () => {
    expect(resultLine({ stack: 'a', services: ['web'], result: 'updated' })).toBe('Updated (web)');
    expect(resultLine({ stack: 'a', services: ['web'], result: 'failed', reason: 'web exited' })).toBe('Update failed (web): web exited');
    expect(resultLine({ stack: 'a', result: 'waiting', reason: 'web: soaking: 3h left' })).toBe('Waiting: web: soaking: 3h left');
    expect(resultLine({ stack: 'a', result: 'skipped', reason: 'stopped' })).toBe('Skipped: stopped');
    expect(resultLine({ stack: 'a', services: ['web'], result: 'rolled back', reason: 'web exited' })).toBe(
      'Update failed (web) and was rolled back: web exited',
    );
  });
  it('sums a run up', () => {
    expect(runSummary({})).toBe('nothing to update');
    expect(runSummary({ paused: true })).toBe('paused, nothing done');
    expect(runSummary({ results: [{ stack: 'a', result: 'updated' }, { stack: 'b', result: 'updated' }, { stack: 'c', result: 'waiting' }] })).toBe(
      '2 updated, 1 waiting',
    );
  });
});
