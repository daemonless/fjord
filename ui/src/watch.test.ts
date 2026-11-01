import { describe, it, expect } from 'vitest';
import { watchStarted } from './watch';

describe('watchStarted', () => {
  // The daemon's exact words (pkg/engine/podman, TestWatchEndedEarlyIsNoVerdict).
  it('sees the line the daemon prints', () => {
    expect(watchStarted('$ podman start app_web_1\n\n[fjord] checking web stays up for 30s\n')).toBe(true);
    expect(watchStarted('[fjord] checking postgres, seerr stay up for 1m0s\n')).toBe(true);
  });
  it('is not fooled by the pull or an app saying "checking"', () => {
    expect(watchStarted('[fjord] still pulling ghcr.io/x (698 MB) · 26s\n')).toBe(false);
    expect(watchStarted('web | checking database connection\n')).toBe(false);
  });
});
