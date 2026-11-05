import { watchStarted } from './watch';

// Each run of an action on a stack has a number. One taken while another is
// down to its stay-up check ends that check; from then on the older run must
// not write into the newer one's output, nor say how the stack ended up.
export function createRunTracker(): { newRun: (name: string) => () => boolean } {
  const runSeq: Record<string, number> = {};
  return {
    newRun(name: string): () => boolean {
      const run = (runSeq[name] = (runSeq[name] ?? 0) + 1);
      return () => runSeq[name] === run;
    },
  };
}

// Read an action's output into the stack's log. At the stay-up check the
// actions come back (see watch.ts). Stops when a newer run takes over;
// false then.
export async function readActionStream(
  res: Response,
  mine: () => boolean,
  onChunk: (chunk: string) => void,
  onWatchStart?: () => void,
): Promise<boolean> {
  const reader = res.body?.getReader();
  if (!reader) return mine();
  const decoder = new TextDecoder('utf-8');
  let full = '';
  let watchingNotified = false;
  while (true) {
    const { done, value } = await reader.read();
    if (!mine()) {
      reader.cancel().catch(() => {});
      return false;
    }
    if (done) return true;
    const chunk = decoder.decode(value);
    full += chunk;
    onChunk(chunk);
    // fjordd counts the changes applied from here (clearPendingAfter):
    // the banner goes now, not after the whole check.
    if (!watchingNotified && onWatchStart && watchStarted(full)) {
      watchingNotified = true;
      onWatchStart();
    }
  }
}

// "pre-flight failed:" alone says nothing; the reason is the next line.
export function formatActionHttpError(why: string, status: number): string {
  return (
    why
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
      .slice(0, 2)
      .join(' ')
      .slice(0, 240) || `HTTP ${status}`
  );
}

export type OutputPollPayload = {
  text?: string;
  action?: string;
  done?: boolean;
};

export type OutputFollowCallbacks = {
  isStreamingHere: () => boolean;
  shouldKeepPolling: () => boolean;
  onUpdate: (o: OutputPollPayload, watching: boolean) => void;
  onFinish: () => void;
  fetchOutput?: (name: string) => Promise<OutputPollPayload | null>;
  intervalMs?: number;
};

// The output of an action this page did not start (another tab, the API,
// start-on-boot) or lost to a reload: fjordd keeps the running or last
// action's output per stack. Shown when this page has nothing of its own,
// and followed while the stack is busy.
// One follow per stack at a time; a second caller waits on the same one,
// so what starts a daemon-side action can wait for its end.
export function createOutputFollower() {
  const inFlight = new Map<string, Promise<void>>();
  return {
    follow(name: string, cb: OutputFollowCallbacks): Promise<void> {
      if (cb.isStreamingHere()) return Promise.resolve();
      const existing = inFlight.get(name);
      if (existing) return existing;
      const fetcher =
        cb.fetchOutput ??
        (async (stackName: string) => {
          const r = await fetch(`/api/stacks/${encodeURIComponent(stackName)}/output`).catch(() => null);
          if (!r?.ok) return null;
          return r.json();
        });
      const waitMs = cb.intervalMs ?? 1500;
      const p = (async () => {
        try {
          for (;;) {
            const o = await fetcher(name);
            if (!o) break;
            if (cb.isStreamingHere()) break; // this page started one meanwhile
            // Down to the stay-up check, the badge says so, as it does for an
            // action this page streamed; the actions are back by then.
            cb.onUpdate(o, !!o.text && watchStarted(o.text));
            if (o.done || !cb.shouldKeepPolling()) break;
            await new Promise((res) => setTimeout(res, waitMs));
          }
        } finally {
          cb.onFinish();
          inFlight.delete(name);
        }
      })();
      inFlight.set(name, p);
      return p;
    },
  };
}
