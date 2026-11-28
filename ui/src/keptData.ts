// App data left on disk by a deleted stack: what the System page lists, and
// what the install wizard finds where a new install would put its own.
import { writable } from 'svelte/store';

export type Leftover = {
  path: string;
  bytes: number;
  more?: boolean;
  /** The ZFS dataset behind the folder, when fjord made one for the app. */
  dataset?: string;
  /** The stack the data belonged to. */
  name?: string;
  appId?: string;
  catalog?: string;
  /** Known only when fjord recorded the delete (0.5.0 on). */
  deletedAt?: string;
  version?: string;
  /** The catalog has the app: it can be installed again on this data. */
  installable?: boolean;
  /** What goes with a dataset when it is removed. */
  restorePoints?: number;
  keptCopies?: number;
  /** Moved out of the way by an install that started fresh; goes on `until`. */
  setAside?: boolean;
  until?: string;
};

/** "Install again", asked on the System page and answered by the App Store. */
export const installAgain = writable<{ appId: string; name: string; catalog?: string } | null>(null);

/** The left-over data an install into base/folder would use, if any. */
export function keptFor(folders: Leftover[] | null | undefined, base: string, folder: string): Leftover | null {
  if (!folders || !base || !folder) return null;
  const want = `${base.replace(/\/+$/, '')}/${folder}`;
  return folders.find((f) => !f.setAside && f.path === want) ?? null;
}

// The leading run of numbers in a version: "v2.1.6-ls123" -> [2, 1, 6].
// Nothing when it does not start with one (a tag like "latest"), or reads as
// a date (2026.11.27), which says nothing about older or newer data formats.
function numbers(version: string): number[] | null {
  const m = /^v?(\d+(?:\.\d+)*)/.exec(version.trim());
  if (!m) return null;
  if (/^v?\d{4}[-.]\d{2}[-.]\d{2}/.test(version.trim())) return null;
  return m[1].split('.').map(Number);
}

/** True only when both are plain versions and `installing` is the lower one. */
export function isOlderVersion(installing: string, kept: string): boolean {
  const a = numbers(installing || '');
  const b = numbers(kept || '');
  if (!a || !b) return false;
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const x = a[i] ?? 0;
    const y = b[i] ?? 0;
    if (x !== y) return x < y;
  }
  return false;
}
