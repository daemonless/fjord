import { describe, it, expect } from 'vitest';
import { staleNote } from './staleNote';

describe('staleNote', () => {
  it('says nothing for a fresh status', () => {
    expect(staleNote({ state: 'running' }, 'podman')).toBe('');
  });
  it('names the engine and when the shown state is from', () => {
    const note = staleNote({ state: 'running', staleSince: '2026-10-05T14:02:31Z' }, 'podman');
    expect(note).toMatch(/^podman is busy · showing the state from /);
  });
  it('says there is no state yet when the engine never answered', () => {
    expect(staleNote({ state: 'unknown', staleSince: '2026-10-05T14:02:31Z' }, 'appjail')).toBe('appjail is busy · no state yet');
  });
});
