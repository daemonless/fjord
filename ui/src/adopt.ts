// Adopting a container someone started outside fjord: one call creates the
// stack from the container's run line (and, with replace, removes the old
// container); a second starts the stack. Shared by the setup wizard and the
// Adopt page so the two can't drift.
export type Candidate = {
  id: string; name: string; image: string; state: string; engine: string;
  service?: string; compose?: string; env?: string; notes?: string[]; error?: string;
  director?: string; makejail?: string; template?: string; project?: string;
};

export async function listCandidates(): Promise<Candidate[]> {
  try {
    const r = await fetch('/api/adopt');
    return r.ok ? await r.json() : [];
  } catch {
    return [];
  }
}

// Returns the new stack id. Throws with a user-facing message.
export async function adoptContainer(c: Candidate, replace: boolean): Promise<string> {
  const r = await fetch('/api/adopt', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ container: c.name, engine: c.engine, replace }),
  });
  if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
  return (await r.json()).id;
}

// Starts a stack and waits for the engine's output to finish. The status is
// sent before the outcome is known, so a failure is a "[error]" line in the
// output -- reading only the status reported a stack that never started
// (navidrome, adopted from a bad run line) as adopted.
export async function startStack(id: string): Promise<void> {
  const r = await fetch(`/api/stacks/${encodeURIComponent(id)}/up`, { method: 'POST' });
  const out = await r.text();
  if (!r.ok) throw new Error(`start ${id}: ${out.trim() || `HTTP ${r.status}`}`);
  const err = startError(out);
  if (err) throw new Error(`start ${id}: ${err}`);
}

// startError is the first "[error]" line of a stack's start output, or ''.
export function startError(out: string): string {
  const line = out.split('\n').find((l) => /^\[error\]/i.test(l));
  return line ? line.replace(/^\[error\]:?\s*/i, '') || 'failed' : '';
}
