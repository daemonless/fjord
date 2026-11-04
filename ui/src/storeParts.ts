export type AppPart = {
  id: string;
  name: string;
  image: string;
};

const fold = (s: string) => s.toLowerCase().replace(/[-_\s]+/g, ' ');

// By name, id, category or stack part, dashes and spaces alike: "uptime-kuma"
// finds Uptime Kuma, and "weavestream api" finds the Weavestream stack.
export function matchesSearch(
  a: { name: string; id: string; category?: string; parts?: AppPart[] },
  search: string
): boolean {
  const q = fold(search.trim());
  if (!q) return true;
  const fields = [a.name, a.id, a.category || '', ...(a.parts?.flatMap((p) => [p.name, p.id]) ?? [])];
  return fields.some((f) => fold(f).includes(q));
}

// Short names for a stack's parts on its card: "Weavestream API" under
// "Weavestream" reads "API".
export function partLabels(appName: string, parts?: AppPart[]): string[] {
  if (!parts?.length) return [];
  const prefix = appName.trim().toLowerCase() + ' ';
  return parts.map((p) => {
    const raw = (p.name || p.id).trim();
    return raw.toLowerCase().startsWith(prefix) ? raw.slice(prefix.length).trim() : raw;
  });
}
