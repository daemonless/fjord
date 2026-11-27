// What fjord recorded about each stack (GET /api/history,
// /api/stacks/<name>/history), and how a line of it reads.

import { isoDateTime } from './when';
export interface HistoryImage {
  ref?: string;
  digest?: string;
}

export interface HistoryChange {
  service: string;
  from?: HistoryImage;
  to?: HistoryImage;
}

export interface HistoryEntry {
  at: string;
  stack: string;
  action: string;
  trigger: string;
  result: 'ok' | 'failed' | string;
  message?: string;
  services?: string[];
  changes?: HistoryChange[];
}

const done: Record<string, string> = {
  install: 'Installed',
  adopt: 'Adopted',
  start: 'Started',
  stop: 'Stopped',
  restart: 'Restarted',
  apply: 'Applied changes',
  update: 'Updated',
  rollback: 'Rolled back',
  delete: 'Deleted',
};

const failed: Record<string, string> = {
  install: 'Install failed',
  adopt: 'Adopt failed',
  start: 'Start failed',
  stop: 'Stop failed',
  restart: 'Restart failed',
  apply: 'Applying changes failed',
  update: 'Update failed',
  rollback: 'Roll back failed',
  delete: 'Delete failed',
};

const by: Record<string, string> = {
  you: '',
  cli: 'from the command line',
  boot: 'when the host started',
  recovery: 'by health recovery',
  automatic: 'automatically',
};

/** The line's headline: "Updated", "Restart failed", plus who did it when it was not you. */
export function historyTitle(e: HistoryEntry): string {
  const what = (e.result === 'failed' ? failed[e.action] : done[e.action]) ?? e.action;
  const who = by[e.trigger] ?? '';
  return who ? `${what} ${who}` : what;
}

/** A tag or short digest for one image: "1.133.0", or "sha256:4f1c2a3b" when the ref has no tag. */
export function imageLabel(img?: HistoryImage): string {
  if (!img) return '';
  const ref = img.ref ?? '';
  const slash = ref.lastIndexOf('/');
  const colon = ref.lastIndexOf(':');
  if (colon > slash && colon >= 0) return ref.slice(colon + 1);
  return img.digest ? img.digest.slice(0, 15) : ref;
}

/** "server 1.132.3 → 1.133.0" for each service whose image changed; a rebuild keeps its tag, so its digest shows. */
export function changeLines(e: HistoryEntry): string[] {
  const out: string[] = [];
  for (const c of e.changes ?? []) {
    if (!c.from || !c.to) continue;
    if (c.from.digest && c.from.digest === c.to.digest) continue;
    let from = imageLabel(c.from);
    let to = imageLabel(c.to);
    if (from === to) {
      from = (c.from.digest ?? '').slice(7, 15);
      to = (c.to.digest ?? '').slice(7, 15);
    }
    out.push(`${c.service} ${from} → ${to}`);
  }
  return out;
}

/** "just now", "3 min ago", then "2026-11-12 09:30". now is passed for tests. */
export function whenLabel(at: string, now: Date = new Date()): string {
  const t = new Date(at);
  const s = Math.round((now.getTime() - t.getTime()) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  return isoDateTime(t);
}
