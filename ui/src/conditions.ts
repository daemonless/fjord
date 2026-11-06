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
  since?: string;
};

export const SNOOZE_KEY = 'fjord.snoozedConditions';
export const SNOOZE_MS = 24 * 60 * 60 * 1000;

export function visibleConditions(
  conditions: Condition[],
  snoozed: Record<string, number>,
  now: number,
): Condition[] {
  const rank = (s: Condition['severity']) => (s === 'critical' ? 0 : s === 'warning' ? 1 : 2);
  return [...(conditions || [])]
    .filter((c) => {
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
