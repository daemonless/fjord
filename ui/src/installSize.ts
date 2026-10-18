// What an install downloads, in the words of the wizard's summary line.
// fjordd resolves the images as install does (POST /api/apps/size) and says
// which are already on the host.

export type SizeAnswer = {
  images: { image: string; bytes?: number; local?: boolean; unknown?: boolean }[];
  download: number;
};

/** A size the way the registry counts it (decimal units), as fjordd's log does. */
export function humanBytes(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)} GB`;
  if (n >= 1e6) return `${Math.round(n / 1e6)} MB`;
  if (n >= 1e3) return `${Math.round(n / 1e3)} kB`;
  return `${n} B`;
}

/** The download for the summary's Download row, or '' when nothing can be said. */
export function sizeWords(a: SizeAnswer | null): string {
  if (!a || !a.images.length) return '';
  const local = a.images.filter((i) => i.local).length;
  if (local === a.images.length) {
    const here = a.images.length === 1 ? 'image already on this host' : `${a.images.length} images already on this host`;
    const bytes = a.images.reduce((n, i) => n + (i.unknown ? 0 : i.bytes ?? 0), 0);
    return bytes > 0 ? `none, ${here} (${humanBytes(bytes)})` : `none, ${here}`;
  }
  if (a.download <= 0) return '';
  const pulled = a.images.length - local;
  const unknown = a.images.filter((i) => !i.local && i.unknown).length;
  const parts = [`${pulled} image${pulled === 1 ? '' : 's'}`];
  if (local) parts.push(`${local} already here`);
  if (unknown) parts.push(`${unknown} of unknown size`);
  return `${humanBytes(a.download)}${a.images.length > 1 || unknown ? ` (${parts.join(', ')})` : ''}`;
}

export const SIZE_NOTE = 'Unpacked on disk, an image takes about two to three times its download.';
