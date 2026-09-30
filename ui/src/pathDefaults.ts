// The default host folder for a path variable, made this stack's own.
//
// A catalog's default names the app ("/containers/librenms/config"), which
// is right for the first copy and wrong for every other: a second librenms
// installed as t-lnms was offered the first one's data folders. A default
// that sits in an App data location under the app's own name is re-rooted
// under this stack's folder; anything else is taken as written.
export function defaultPath(
  def: string,
  o: { appId: string; locations: string[]; base: string; slug: string; hint: string },
): string {
  const d = (def || '').trim();
  if (!d) return o.hint;
  if (!o.appId) return d;
  const roots = Array.from(new Set([...o.locations, o.base, '/containers'].filter(Boolean).map((l) => l.replace(/\/+$/, ''))));
  for (const root of roots) {
    const own = `${root}/${o.appId}`;
    if (d === own) return `${o.base.replace(/\/+$/, '')}/${o.slug}`;
    if (d.startsWith(own + '/')) return `${o.base.replace(/\/+$/, '')}/${o.slug}/${d.slice(own.length + 1)}`;
  }
  return d;
}
