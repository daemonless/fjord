// What the version menu offers for an image: the catalog's variants that the
// registry publishes (or, with no catalog variants, the registry's own
// channels), each as "tag · newest release", the recommended one first.
// Grouped by release line only when a line has more than one build
// (haproxy 3.4: plain, lua, wolfssl); a line per variant (python, redis)
// stays one list, newest first.

export type Variant = { id: string; label?: string; default?: boolean; version?: string };
export type Pin = { version: string; tag: string };
export type Choice = { id: string; text: string; desc: string; rec: boolean; newest: string };
export type Group = { line: string; choices: Choice[] };

// The release line a tag starts with: "15-latest" -> "15", "3.4-lua-pkg-latest"
// -> "3.4", "313t-pkg-latest" -> "313t"; "" for "pkg", "latest", "devel-pkg".
export function releaseLine(id: string): string {
  return id.match(/^v?(\d+(?:\.\d+)*t?)(?:-|$)/)?.[1] ?? '';
}

// Newest first: 3.4 before 3.3 before 2.8, 17 before 16; a line that is not
// a number goes last.
function lineOrder(a: string, b: string): number {
  if (!a || !b) return a ? -1 : b ? 1 : 0;
  const pa = a.match(/\d+/g)!.map(Number);
  const pb = b.match(/\d+/g)!.map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pb[i] ?? 0) - (pa[i] ?? 0);
    if (d) return d;
  }
  return a.localeCompare(b);
}

export function variantChoices(variants: Variant[], trains: Record<string, Pin[]>): Group[] {
  const loaded = Object.keys(trains).length > 0;
  const source: Variant[] = variants.length
    ? variants.filter((v) => !loaded || trains[v.id]) // only what is published, once that is known
    : Object.keys(trains).map((id) => ({ id }));
  const choices: Choice[] = source.map((v) => {
    const newest = trains[v.id]?.[0]?.version || v.version || '';
    const rec = !!v.default;
    return {
      id: v.id,
      newest,
      rec,
      desc: v.label && v.label !== v.id ? v.label : '',
      text: `${v.id}${newest ? ` · ${newest}` : ''}${rec ? '  (recommended)' : ''}`,
    };
  });
  const recFirst = (a: Choice, b: Choice) => Number(b.rec) - Number(a.rec);
  const byLine = new Map<string, Choice[]>();
  for (const c of choices) {
    const l = releaseLine(c.id);
    byLine.set(l, [...(byLine.get(l) ?? []), c]);
  }
  const numbered = [...byLine.keys()].filter(Boolean);
  const grouped = numbered.length > 1 && numbered.some((l) => byLine.get(l)!.length > 1);
  if (!grouped) {
    const list = [...choices];
    // All numbered: newest line first. Otherwise the image's own order.
    if (list.every((c) => releaseLine(c.id))) list.sort((a, b) => lineOrder(releaseLine(a.id), releaseLine(b.id)));
    return [{ line: '', choices: list.sort(recFirst) }];
  }
  return [...byLine.keys()]
    .sort(lineOrder)
    .map((line) => ({ line, choices: [...byLine.get(line)!].sort(recFirst) }));
}

// Every choice in menu order, for a default pick or a count.
export const flatChoices = (groups: Group[]): Choice[] => groups.flatMap((g) => g.choices);
