// Whether a stack needs attention, for the sidebar filter and the dashboard's
// one-line summary. A stack is judged against what it is MEANT to do: one the
// operator stopped is fine stopped; one meant to run that isn't is a problem.

export type HealthStack = {
  name: string;
  displayName?: string;
  status?: { state?: string; containers?: { name?: string; service?: string; state?: string; detail?: string }[] };
  state?: { desired_state?: string; last_failure?: { action: string; message?: string } };
  /** What fjord is doing to it right now. An install or up shows no container
   *  for the whole pull, which read as "stopped" -- a problem -- until done. */
  busy?: string;
};

/** failedAction is the action fjord last saw end in an error on s, until the
 *  next one of it ends clean or it is dismissed. Health has its own words. */
export function failedAction(s: HealthStack): { label: string; verb: string; message: string } | null {
  const f = s.state?.last_failure;
  if (s.busy || !f || f.action === 'health') return null;
  const message = f.message ?? '';
  switch (f.action) {
    case 'update':
      return { label: 'Update failed', verb: 'could not update', message };
    case 'up':
      return { label: 'Start failed', verb: 'could not start', message };
    case 'start on boot':
      return { label: 'Start on boot failed', verb: 'did not start on boot', message };
    case 'install':
      return { label: 'Install failed', verb: 'did not install', message };
    case 'down':
      return { label: 'Stop failed', verb: 'could not stop', message };
    case 'restart':
      return { label: 'Restart failed', verb: 'could not restart', message };
  }
  return { label: 'Failed', verb: 'had an action fail', message };
}

/** problem says, in a few words, what is wrong with a stack meant to run -- or ''. */
export function problem(s: HealthStack): string {
  const now = statusProblem(s);
  return now || (failedAction(s)?.verb ?? '');
}

function statusProblem(s: HealthStack): string {
  if (s.state?.desired_state === 'stopped' || s.busy) return '';
  const st = s.status?.state;
  if (!st || st === 'running') {
    // Running, but a container may still be crash-looping (restart: always
    // reads "running" between crashes; the daemon says so in detail).
    const loop = (s.status?.containers ?? []).find((c) => /crash|restart/i.test(`${c.state} ${c.detail ?? ''}`));
    return loop ? 'is restarting' : '';
  }
  if (st === 'partial') {
    const down = (s.status?.containers ?? []).filter((c) => c.state !== 'running');
    if (down.length > 0 && down.every((c) => c.state === 'crashed' || /^crash-looping/i.test(c.detail ?? ''))) {
      return 'is restarting';
    }
    const names = down.map((c) => c.service || c.name);
    return names.length ? `is partly down (${names.join(', ')})` : 'is partly down';
  }
  if (st === 'stopped') return 'is stopped';
  return `is ${st}`;
}

export type Health = 'running' | 'stopped' | 'problem';

/** health buckets a stack for the filter. */
export function health(s: HealthStack): Health {
  if (s.busy) return s.busy === 'down' || s.busy === 'deleting' ? 'stopped' : 'running';
  if (problem(s)) return 'problem';
  return s.status?.state === 'running' || s.status?.state === 'partial' ? 'running' : 'stopped';
}
