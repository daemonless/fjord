// Pure helpers for Restore points UI (PR 2b). User-visible sentences stay in
// Svelte templates so wuchale extracts them for translation.

export function notCoveredParts(paths?: string[]): { shown: string; extra: number } | null {
  if (!paths || paths.length === 0) return null;
  if (paths.length <= 3) {
    return { shown: paths.join(', '), extra: 0 };
  }
  return { shown: paths.slice(0, 3).join(', '), extra: paths.length - 3 };
}

export function zfsHintMountpoint(hint?: string): string {
  if (!hint) return '/apps';
  const m = hint.match(/mountpoint=(\S+)/);
  return m?.[1] || '/apps';
}

export function isRestorePointKeptForever(snap: { kind: string; keepForever?: boolean }): boolean {
  return snap.kind === 'manual' || !!snap.keepForever;
}

export function canToggleRestorePointKeep(snap: { kind: string }): boolean {
  return snap.kind !== 'manual';
}
