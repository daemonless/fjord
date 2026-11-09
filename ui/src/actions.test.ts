import { describe, expect, it } from 'vitest';
import {
  actionOutcome,
  createOutputFollower,
  createRunTracker,
  formatActionHttpError,
  hasErrorLine,
  latest,
  readActionStream,
} from './actions';

function makeStreamResponse(chunks: string[]): Response {
  const enc = new TextEncoder();
  let idx = 0;
  const stream = new ReadableStream<Uint8Array>({
    pull(controller) {
      if (idx < chunks.length) {
        controller.enqueue(enc.encode(chunks[idx++]));
      } else {
        controller.close();
      }
    },
  });
  return new Response(stream);
}

describe('actions helpers', () => {
  it('tracks per-stack run ownership and invalidates older runs', () => {
    const tracker = createRunTracker();
    const first = tracker.newRun('radarr');
    const other = tracker.newRun('sonarr');
    expect(first()).toBe(true);
    expect(other()).toBe(true);

    const second = tracker.newRun('radarr');
    expect(first()).toBe(false);
    expect(second()).toBe(true);
    expect(other()).toBe(true);
  });

  it('latest() invalidates earlier calls when a newer one starts', () => {
    const next = latest();
    const first = next();
    expect(first()).toBe(true);
    const second = next();
    expect(first()).toBe(false);
    expect(second()).toBe(true);
  });

  it('decodes multi-byte UTF-8 characters split across stream chunks', async () => {
    const tracker = createRunTracker();
    const mine = tracker.newRun('radarr');
    const bytes = new TextEncoder().encode('Checking — ok 🚀\n');
    // Split inside both the em-dash (3 bytes) and the rocket emoji (4 bytes).
    const parts = [bytes.slice(0, 10), bytes.slice(10, 16), bytes.slice(16)];
    let idx = 0;
    const res = new Response(
      new ReadableStream<Uint8Array>({
        pull(controller) {
          if (idx < parts.length) controller.enqueue(parts[idx++]);
          else controller.close();
        },
      }),
    );
    let out = '';
    const ok = await readActionStream(res, mine, (chunk) => {
      out += chunk;
    });
    expect(ok).toBe(true);
    expect(out).toBe('Checking — ok 🚀\n');
  });

  it('reads streamed action chunks and fires onWatchStart once when stay-up watch begins', async () => {
    const tracker = createRunTracker();
    const mine = tracker.newRun('radarr');
    const res = makeStreamResponse([
      '$ podman-compose up -d\n',
      '[fjord] checking radarr stays up for 30s\n',
      '[fjord] radarr stayed up\n',
    ]);
    let collected = '';
    let watchCalls = 0;
    const finished = await readActionStream(
      res,
      mine,
      (chunk) => {
        collected += chunk;
      },
      () => {
        watchCalls++;
      },
    );
    expect(finished).toBe(true);
    expect(watchCalls).toBe(1);
    expect(collected).toContain('[fjord] radarr stayed up');
  });

  it('aborts reading when a newer run takes over the stack', async () => {
    const tracker = createRunTracker();
    const mine = tracker.newRun('radarr');
    const res = makeStreamResponse(['chunk-1\n', 'chunk-2\n']);
    let seen = 0;
    const finished = await readActionStream(res, mine, () => {
      seen++;
      tracker.newRun('radarr');
    });
    expect(finished).toBe(false);
    expect(seen).toBe(1);
  });

  it('formats multi-line HTTP errors with the reason line included', () => {
    expect(formatActionHttpError('pre-flight failed:\nport 8080 is held by radarr', 409)).toBe(
      'pre-flight failed: port 8080 is held by radarr',
    );
    expect(formatActionHttpError('', 500)).toBe('HTTP 500');
  });

  it('deduplicates concurrent output followers per stack and stops when done', async () => {
    const follower = createOutputFollower();
    const polls: number[] = [];
    const states = [
      { text: '$ pull\n', action: 'update', done: false },
      { text: '$ pull\n[fjord] checking radarr stays up for 30s\n', action: 'update', done: false },
      { text: '$ pull\n[fjord] checking radarr stays up for 30s\n[fjord] ok\n', action: 'update', done: true },
    ];
    let step = 0;
    const updates: { text?: string; watching: boolean }[] = [];
    let finishedCount = 0;

    const cb = {
      isStreamingHere: () => false,
      shouldKeepPolling: () => true,
      intervalMs: 1,
      fetchOutput: async () => {
        polls.push(step);
        return states[step++] ?? null;
      },
      onUpdate: (o: { text?: string }, watching: boolean) => {
        updates.push({ text: o.text, watching });
      },
      onFinish: () => {
        finishedCount++;
      },
    };

    const p1 = follower.follow('radarr', cb);
    const p2 = follower.follow('radarr', cb);
    expect(p1).toBe(p2);
    await Promise.all([p1, p2]);

    expect(polls).toEqual([0, 1, 2]);
    expect(updates[0].watching).toBe(false);
    expect(updates[1].watching).toBe(true);
    expect(finishedCount).toBe(1);
  });

  it('detects [error] lines in install and action streams and marks outcome failed', () => {
    expect(hasErrorLine('Installing radarr...\n[fjord] radarr stayed up\n')).toBe(false);
    expect(actionOutcome('Installing radarr...\n[fjord] radarr stayed up\n')).toEqual({
      status: 'idle',
      message: '',
    });

    const failedInstall = 'Installing radarr...\n[error] up failed: port 7878 is in use\n';
    expect(hasErrorLine(failedInstall)).toBe(true);
    expect(actionOutcome(failedInstall, 'Failed — see Output')).toEqual({
      status: 'error',
      message: 'up failed: port 7878 is in use',
    });
  });

  it('keeps polling until done when untilDone is set and tolerates transient failed polls', async () => {
    const follower = createOutputFollower();
    const responses = [
      { text: '$ pull\n', action: 'update', done: false },
      null,
      { text: '$ pull\n[error] pull failed\n', action: 'update', done: true },
    ];
    let idx = 0;
    const last = await follower.follow('radarr', {
      isStreamingHere: () => false,
      shouldKeepPolling: () => false,
      untilDone: true,
      maxFailures: 1,
      intervalMs: 1,
      fetchOutput: async () => responses[idx++] ?? null,
      onUpdate: () => {},
      onFinish: () => {},
    });
    expect(last?.done).toBe(true);
    expect(hasErrorLine(last?.text || '')).toBe(true);
  });

  it('ignores previous completed action output when expectedAction is set', async () => {
    const follower = createOutputFollower();
    const responses = [
      { text: '$ up\n[fjord] ok\n', action: 'up', done: true },
      { text: '$ pull\n', action: 'update', done: false },
      { text: '$ pull\n[fjord] updated\n', action: 'update', done: true },
    ];
    let idx = 0;
    const seen: string[] = [];
    const last = await follower.follow('radarr', {
      isStreamingHere: () => false,
      shouldKeepPolling: () => true,
      untilDone: true,
      expectedAction: 'update',
      maxFailures: 2,
      intervalMs: 1,
      fetchOutput: async () => responses[idx++] ?? null,
      onUpdate: (o) => {
        if (o.text) seen.push(o.text);
      },
      onFinish: () => {},
    });
    expect(last?.action).toBe('update');
    expect(last?.done).toBe(true);
    expect(seen).toEqual(['$ pull\n', '$ pull\n[fjord] updated\n']);
  });
});
