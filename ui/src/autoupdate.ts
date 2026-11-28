// Automatic updates as the Updates settings and a stack's Settings tab
// show them (cmd/fjordd/autoupdate.go).

export type AutoResult = { stack: string; services?: string[]; result: 'updated' | 'rolled back' | 'failed' | 'waiting' | 'skipped'; reason?: string };
export type AutoRun = { at?: string; trigger?: string; window?: string; results?: AutoResult[]; paused?: boolean };
export type AutoUpdate = { start: string; end: string; defaultForNew?: boolean; stacksOn?: number; stacksTotal?: number; running: boolean; next: string; last: AutoRun; paused?: string };

/** On is any policy that applies updates by itself. */
export function keepsUpToDate(policy: string | undefined): boolean {
  return !!policy && policy !== 'off' && policy !== 'notify';
}

/** One line for what happened to a stack in a run. */
export function resultLine(r: AutoResult): string {
  const what = r.services?.length ? ` (${r.services.join(', ')})` : '';
  switch (r.result) {
    case 'updated':
      return `Updated${what}`;
    case 'rolled back':
      return r.reason ? `Update failed${what} and was rolled back: ${r.reason}` : `Update failed${what} and was rolled back`;
    case 'failed':
      return r.reason ? `Update failed${what}: ${r.reason}` : `Update failed${what}`;
    case 'waiting':
      return r.reason ? `Waiting: ${r.reason}` : 'Waiting';
    default:
      return r.reason ? `Skipped: ${r.reason}` : 'Skipped';
  }
}

/** The run in a few words: "2 updated, 1 waiting", or "nothing to update". */
export function runSummary(run: AutoRun): string {
  const rs = run.results ?? [];
  if (!rs.length) return run.paused ? 'paused, nothing done' : 'nothing to update';
  const n = (k: AutoResult['result']) => rs.filter((r) => r.result === k).length;
  return (['updated', 'rolled back', 'failed', 'waiting', 'skipped'] as const)
    .filter((k) => n(k) > 0)
    .map((k) => `${n(k)} ${k}`)
    .join(', ');
}
