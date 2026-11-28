// Pure types and helpers for counted unattended failure notices (PR 4).
// User-visible sentences stay in Svelte templates so wuchale extracts them.

export type NoticeKind = 'daily-restore-point' | 'auto-update' | 'auto-update-no-snapshot';

export type FailureNotice = {
  kind: NoticeKind;
  count: number;
  firstAt: string;
  lastAt: string;
  message?: string;
  dismissed?: boolean;
};

export type FleetNotice = FailureNotice & {
  stack: string;
  displayName?: string;
};

export function activeNotices(notices?: FailureNotice[]): FailureNotice[] {
  return (notices ?? []).filter((n) => !n.dismissed);
}

export function hasActiveNotices(notices?: FailureNotice[]): boolean {
  return activeNotices(notices).length > 0;
}

const KIND_ORDER: NoticeKind[] = ['daily-restore-point', 'auto-update', 'auto-update-no-snapshot'];

// Groups all fleet notices (including dismissed ones) by kind in a stable order
// for the Settings -> Maintenance summary.
export function groupNoticesByKind(notices?: FleetNotice[]): { kind: NoticeKind; items: FleetNotice[] }[] {
  const byKind = new Map<NoticeKind, FleetNotice[]>();
  for (const n of notices ?? []) {
    const list = byKind.get(n.kind) ?? [];
    list.push(n);
    byKind.set(n.kind, list);
  }
  const out: { kind: NoticeKind; items: FleetNotice[] }[] = [];
  for (const kind of KIND_ORDER) {
    const items = byKind.get(kind);
    if (items && items.length > 0) out.push({ kind, items });
  }
  return out;
}

// A failed automatic update is told by its notice. The plain "Update failed"
// banner for that same failure stays away while the notice exists, dismissed
// or not: dismissing the notice must not bring the same failure back in other
// words. A later failure (an update by hand) is newer than the notice and
// shows as usual.
export function toldByNotice(failure: { at?: string } | null | undefined, notices?: FailureNotice[]): boolean {
  const at = Date.parse(failure?.at ?? '');
  if (Number.isNaN(at)) return false;
  return (notices ?? []).some(
    (n) => (n.kind === 'auto-update' || n.kind === 'auto-update-no-snapshot') && at <= Date.parse(n.lastAt) + 60_000,
  );
}
