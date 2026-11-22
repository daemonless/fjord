export type Condition = {
  id: string;
  severity: 'critical' | 'warning' | 'info';
  scope: 'host' | 'engine' | 'network' | 'stack';
  engine?: string;
  title: string;
  detail?: string;
  why?: string;
  action?: string;
  commands?: string[];
  fix?: string;
  installable?: boolean;
  // Only a browser running on the host itself is shown it.
  onHost?: boolean;
  since?: string;
};

export const SNOOZE_KEY = 'fjord.snoozedConditions';
export const SNOOZE_MS = 24 * 60 * 60 * 1000;

// browserOnHost: the page was opened at a loopback name, so the browser runs
// on the host fjord manages.
export function browserOnHost(hostname: string): boolean {
  const h = hostname.toLowerCase().replace(/^\[|\]$/g, '');
  return h === 'localhost' || h.endsWith('.localhost') || h === '::1' || /^127\.\d+\.\d+\.\d+$/.test(h);
}

export function visibleConditions(
  conditions: Condition[],
  snoozed: Record<string, number>,
  now: number,
  onHost = false,
): Condition[] {
  const rank = (s: Condition['severity']) => (s === 'critical' ? 0 : s === 'warning' ? 1 : 2);
  return [...(conditions || [])]
    .filter((c) => {
      if (c.onHost && !onHost) return false;
      if (c.severity === 'critical') return true;
      const until = snoozed[c.id];
      return !until || until <= now;
    })
    .sort((a, b) => rank(a.severity) - rank(b.severity) || a.id.localeCompare(b.id));
}

export function loadSnoozed(raw: string | null, now: number): Record<string, number> {
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object') return {};
    const out: Record<string, number> = {};
    for (const [k, v] of Object.entries(parsed)) {
      if (typeof v === 'number' && v > now) out[k] = v;
    }
    return out;
  } catch {
    return {};
  }
}
