// What an update changes, in one sentence for the Update panel. It used to be
// a log line -- "rebuild · same version (10.11.19) · built Sep 21 → Sep 22 ·
// no package changes" -- with raw digests beside it; the digests now live in
// a tooltip, and the sentence says what the operator is deciding on.

export type Changes = {
  class?: string; // rebuild | patch | minor | major | unknown
  versionFrom?: string;
  versionTo?: string;
  createdFrom?: string;
  createdTo?: string;
  packages: boolean;
  changed?: unknown[];
  added?: unknown[];
  removed?: unknown[];
};

const day = (iso?: string) =>
  iso ? new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' }) : '';

const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`;

/** packagesPhrase: "no package changes", "1 package changed, 2 added", or "". */
export function packagesPhrase(c: Changes): string {
  if (!c.packages) return '';
  const changed = c.changed?.length ?? 0;
  const added = c.added?.length ?? 0;
  const removed = c.removed?.length ?? 0;
  if (!changed && !added && !removed) return 'no package changes';
  const bits: string[] = [];
  if (changed) bits.push(`${plural(changed, 'package')} changed`);
  if (added) bits.push(`${changed ? added : plural(added, 'package')} added`);
  if (removed) bits.push(`${changed || added ? removed : plural(removed, 'package')} removed`);
  return bits.join(', ');
}

/** changeSentence says what installing the update would change. */
export function changeSentence(c: Changes): string {
  const from = c.versionFrom;
  const to = c.versionTo;
  const built = c.createdTo && day(c.createdTo) !== day(c.createdFrom) ? `built ${day(c.createdTo)}` : '';
  let head: string;
  switch (c.class) {
    case 'rebuild':
      head = to ? `New build of the same version (${to})` : 'New build of the same version';
      break;
    case 'patch':
    case 'minor':
    case 'major':
      head = `New ${c.class} version` + (from && to ? `, ${from} → ${to}` : to ? `, ${to}` : '');
      break;
    default:
      head = to && from && to !== from ? `Newer image, ${from} → ${to}` : to ? `Newer image (${to})` : 'Newer image';
  }
  const tail = [built, packagesPhrase(c)].filter(Boolean);
  if (c.class === 'major') tail.push('read its release notes first');
  if (!tail.length) return head;
  // "built <day>" joins the head with a comma; the rest follows a dash.
  const [first, ...rest] = built ? tail : ['', ...tail];
  return head + (first ? `, ${first}` : '') + (rest.length ? ` — ${rest.join(' — ')}` : '');
}
