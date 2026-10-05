// What the page says when the engine did not answer in time and the status
// shown is the last one it gave (status.staleSince, set by the daemon).
// A clock time, not "40 s ago": nothing re-renders while the engine is busy,
// so an age would freeze and lie.
export function staleNote(
  status: { state?: string; staleSince?: string } | undefined,
  engine: string | undefined,
): string {
  if (!status?.staleSince) return '';
  const who = engine || 'the engine';
  if (status.state === 'unknown') return `${who} is busy · no state yet`;
  const at = new Date(status.staleSince);
  if (isNaN(at.getTime())) return `${who} is busy · showing the last state`;
  return `${who} is busy · showing the state from ${at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}`;
}
