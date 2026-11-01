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

/**
 * statusKey is the word a stack's status is shown as. "checking" when fjord
 * has no answer from the engine yet -- not loaded, or the engine busy since
 * fjordd started. That used to read "unknown" in red, which looks like a
 * broken app when nothing is known yet (Crisps, during pulls). "unknown" is
 * left for an engine that answered and could not say (its socket down).
 */
export function statusKey(status: { state?: string; staleSince?: string } | undefined): string {
  if (!status?.state) return 'checking';
  if (status.state === 'unknown' && status.staleSince) return 'checking';
  return status.state;
}
