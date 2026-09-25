// Which folder set an app's folder variable gets at install, from the set's
// keywords (Match, "AUDIOBOOK" or "MOVIE|FILM").
//
// Keywords used to match anywhere in the variable name, so "BOOK" took
// AUDIOBOOKS_PATH (an audiobook app got the ebook library) and
// BOOKDROP_LOCATION (an import drop folder got the whole library). Now a
// keyword must be a whole word of the name -- split at "_" -- alone or with
// an S: BOOK is BOOK or BOOKS, never AUDIOBOOKS or BOOKDROP. A name that
// matches nothing is tried against the variable's label the same way -- but
// only a label that calls the folder a library: readmeabook's MEDIA_PATH
// ("Audiobook library") finds Audiobooks, while immich-server's DATA_PATH
// ("Media storage (photos, videos, thumbnails)") is immich's own storage and
// must never be handed the Photos library to write thumbnails into.

type Set = { match?: string; folders: string[] };

const words = (s: string) => s.toUpperCase().split(/[^A-Z0-9]+/).filter(Boolean);

function hits(ws: string[], match: string): boolean {
  const keys = match.split('|').map((k) => k.trim().toUpperCase()).filter(Boolean);
  return ws.some((w) => keys.some((k) => w === k || w === k + 'S'));
}

/** setFor is the first set with folders whose keywords match, or undefined. */
export function setFor<T extends Set>(name: string, label: string | undefined, sets: T[]): T | undefined {
  const usable = sets.filter((s) => s.match && s.folders.length);
  const byName = words(name);
  const byLabel = label ? words(label) : [];
  return (
    usable.find((s) => hits(byName, s.match!)) ??
    (byLabel.includes('LIBRARY') ? usable.find((s) => hits(byLabel, s.match!)) : undefined)
  );
}
