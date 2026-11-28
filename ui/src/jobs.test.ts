import { describe, expect, it } from 'vitest';
import { jobPercent, jobStepLine, runningJobOf, type BgJob } from './jobs';

const baseJob: BgJob = {
  id: 'migrate-all-1',
  kind: 'migrate-all',
  title: 'Moving apps to ZFS datasets',
  status: 'running',
  startedAt: '2026-11-27T18:00:00Z',
  total: 4,
  done: 1,
  current: 'radarr',
  phase: 'Copying',
  bytesDone: 5 * 1024 ** 3,
  bytesTotal: 10 * 1024 ** 3,
};

describe('background jobs helpers', () => {
  it('combines completed items and live copied bytes into smooth percent', () => {
    expect(jobPercent(baseJob)).toBe(38); // (1 + 0.5) / 4 = 37.5% -> 38%
    expect(jobPercent({ ...baseJob, total: 0, done: 0 })).toBeNull();
    expect(jobPercent({ ...baseJob, status: 'done', done: 4, total: 4 })).toBe(100);
    expect(jobPercent({ ...baseJob, status: 'done', done: 0, total: 0 })).toBe(100);
  });

  it('formats live step lines with byte progress while copying', () => {
    expect(jobStepLine(baseJob)).toBe('radarr · Copying 5.0 GB / 10.0 GB');
    expect(jobStepLine({ ...baseJob, phase: 'Stopping', bytesDone: 0 })).toBe('radarr · Stopping');
    expect(jobStepLine({ ...baseJob, current: '', phase: 'Scanning eligible apps', bytesTotal: 0 })).toBe('Scanning eligible apps');
    expect(jobStepLine({ ...baseJob, status: 'done', summary: 'Moved 4 of 4 apps to ZFS datasets' })).toBe(
      'Moved 4 of 4 apps to ZFS datasets',
    );
  });

  it('finds active running job by kind', () => {
    const done = { ...baseJob, id: 'migrate-all-0', status: 'done' as const };
    expect(runningJobOf([done], 'migrate-all')).toBeUndefined();
    expect(runningJobOf([done, baseJob], 'migrate-all')?.id).toBe('migrate-all-1');
  });
});
