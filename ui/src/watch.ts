// After an update or install the daemon watches the new containers for a
// while, and announces it with this line (engine.WatchStartPrefix). Only the
// check is left then, and nothing needs it to finish: the page gives the
// actions back, and taking one ends the check, as Ctrl-C would.
const WATCH_START = /^\[fjord\] checking .+ up for /m;

/** watchStarted: the output has reached the stay-up check. */
export function watchStarted(log: string): boolean {
  return WATCH_START.test(log || '');
}
