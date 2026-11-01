import { describe, it, expect } from 'vitest';
import { staleNote, statusKey } from './staleNote';

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

describe('statusKey', () => {
  it('is checking before fjord has any status', () => {
    expect(statusKey(undefined)).toBe('checking');
  });
  it('is checking while a busy engine has never answered', () => {
    expect(statusKey({ state: 'unknown', staleSince: '2026-10-05T14:02:31Z' })).toBe('checking');
  });
  it('keeps unknown for an engine that answered and could not say', () => {
    expect(statusKey({ state: 'unknown' })).toBe('unknown');
  });
  it('shows the last known state while the engine is busy', () => {
    expect(statusKey({ state: 'running', staleSince: '2026-10-05T14:02:31Z' })).toBe('running');
  });
});
