// The status beside a failed action: the reason itself, not "HTTP 409" or
// "see Output". The full text stays in Output.

const MAX = 160;

/** firstLine: the first non-empty line of a message, cut to fit a status. */
export function firstLine(msg: string): string {
  const line = (msg || '').split('\n').map((l) => l.trim()).find((l) => l) ?? '';
  return line.length > MAX ? line.slice(0, MAX - 1) + '…' : line;
}
