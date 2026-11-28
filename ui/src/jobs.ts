import { writable } from 'svelte/store';

export type BgJobItemStatus = 'queued' | 'running' | 'done' | 'skipped' | 'failed';

export type BgJobItem = {
  name: string;
  status: BgJobItemStatus;
  detail?: string;
};

export type BgJobStatus = 'running' | 'done' | 'failed' | 'stopped';

export type BgJob = {
  id: string;
  kind: 'migrate-all' | 'migrate-dataset' | 'update-all' | 'auto-update' | 'restore-points' | string;
  title: string;
  status: BgJobStatus;
  stopping?: boolean;
  cancellable?: boolean;
  startedAt: string;
  finishedAt?: string;
  total: number;
  done: number;
  current?: string;
  phase?: string;
  bytesDone?: number;
  bytesTotal?: number;
  summary?: string;
  items?: BgJobItem[];
};

export const jobsStore = writable<BgJob[]>([]);

export function applyJobsEvent(list: BgJob[] | null | undefined) {
  jobsStore.set(Array.isArray(list) ? list : []);
}

export async function loadJobs() {
  try {
    const r = await fetch('/api/jobs');
    if (r.ok) {
      const d = await r.json();
      applyJobsEvent(d?.jobs);
    }
  } catch {}
}

export async function cancelJob(id: string): Promise<boolean> {
  try {
    const r = await fetch(`/api/jobs/${encodeURIComponent(id)}/cancel`, { method: 'POST' });
    return r.ok;
  } catch {
    return false;
  }
}

export async function dismissJob(id: string): Promise<boolean> {
  try {
    const r = await fetch(`/api/jobs/${encodeURIComponent(id)}/dismiss`, { method: 'POST' });
    if (r.ok) {
      jobsStore.update((list) => list.filter((j) => j.id !== id));
      return true;
    }
  } catch {}
  return false;
}

export function fmtJobBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(0)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(1)} GB`;
}

/** Progress percentage (0..100), or null when total is not known yet. */
export function jobPercent(job: BgJob): number | null {
  if (job.status !== 'running') {
    if (job.total > 0) return Math.min(100, Math.round((job.done / job.total) * 100));
    return 100;
  }
  if (!job.total || job.total <= 0) return null;
  let frac = job.done;
  if (job.bytesTotal && job.bytesTotal > 0 && job.bytesDone && job.bytesDone > 0) {
    frac += Math.min(0.99, job.bytesDone / job.bytesTotal);
  }
  return Math.max(0, Math.min(100, Math.round((frac / job.total) * 100)));
}

/** One-line live step description for a running job. */
export function jobStepLine(job: BgJob): string {
  if (job.status !== 'running') return job.summary || '';
  const parts: string[] = [];
  if (job.current) parts.push(job.current);
  if (job.phase) {
    if (job.bytesTotal && job.bytesTotal > 0 && job.phase.toLowerCase().startsWith('copy')) {
      const done = Math.min(job.bytesDone || 0, job.bytesTotal);
      parts.push(`${job.phase} ${fmtJobBytes(done)} / ${fmtJobBytes(job.bytesTotal)}`);
    } else {
      parts.push(job.phase);
    }
  }
  if (parts.length === 0) return 'Working…';
  return parts.join(' · ');
}

export function runningJobOf(list: BgJob[], kind: string): BgJob | undefined {
  return list.find((j) => j.kind === kind && j.status === 'running');
}
