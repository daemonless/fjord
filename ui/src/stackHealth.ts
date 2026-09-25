// Whether a stack needs attention, for the sidebar filter and the dashboard's
// one-line summary. A stack is judged against what it is MEANT to do: one the
// operator stopped is fine stopped; one meant to run that isn't is a problem.

export type HealthStack = {
  name: string;
  displayName?: string;
  status?: { state?: string; containers?: { name?: string; service?: string; state?: string; detail?: string }[] };
  state?: { desired_state?: string };
};

/** problem says, in a few words, what is wrong with a stack meant to run -- or ''. */
export function problem(s: HealthStack): string {
  if (s.state?.desired_state === 'stopped') return '';
  const st = s.status?.state;
  if (!st || st === 'running') {
    // Running, but a container may still be crash-looping (restart: always
    // reads "running" between crashes; the daemon says so in detail).
    const loop = (s.status?.containers ?? []).find((c) => /crash|restart/i.test(`${c.state} ${c.detail ?? ''}`));
    return loop ? 'is restarting' : '';
  }
  if (st === 'partial') {
    const down = (s.status?.containers ?? []).filter((c) => c.state !== 'running').map((c) => c.service || c.name);
    return down.length ? `is partly down (${down.join(', ')})` : 'is partly down';
  }
  if (st === 'stopped') return 'is stopped';
  return `is ${st}`;
}

export type Health = 'running' | 'stopped' | 'problem';

/** health buckets a stack for the filter. */
export function health(s: HealthStack): Health {
  if (problem(s)) return 'problem';
  return s.status?.state === 'running' || s.status?.state === 'partial' ? 'running' : 'stopped';
}
