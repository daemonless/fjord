// App icons for stack rows: catalog id -> icon URL, loaded once and shared by
// the sidebar and the stack list so both show the same picture, plus the
// deterministic letter-tile colour used when an app has no icon.
import { writable, get } from 'svelte/store';

export const appIcons = writable<Record<string, string>>({});
// The same icons by image repo, for a stack's services: each of
// crafting-apps' ten is an app of its own in the catalog.
export const imageIcons = writable<Record<string, string>>({});

let loaded = false;
export async function loadAppIcons() {
  if (loaded) return;
  loaded = true;
  try {
    const r = await fetch('/catalog/catalog.json');
    if (r.ok) {
      const d = await r.json();
      const m: Record<string, string> = {};
      for (const a of d.apps || []) m[a.id] = a.icon;
      appIcons.set(m);
      imageIcons.set(iconsByImage(d.apps || []));
    }
  } catch {
    loaded = false; // let a later caller retry
  }
}

// Icon for a stack: the copy kept in its own directory (survives the app
// leaving the catalog), else the live catalog by app id, else by display name
// (stacks installed before app_id was recorded whose name matches an id).
// "" = none, show a letter tile.
export function iconFor(
  icons: Record<string, string>,
  s: { name: string; icon?: string; displayName?: string; state?: { origin?: { app_id?: string } } },
): string {
  if (s.icon) return `/api/stacks/${encodeURIComponent(s.name)}/icon?v=${encodeURIComponent(s.icon)}`;
  return icons[s.state?.origin?.app_id ?? ''] || icons[(s.displayName ?? '').toLowerCase()] || '';
}

// iconsByImage maps each catalog image repo to its app's icon. A stack's
// entry names its first service's image (crafting-apps -> photocraft), so
// when two apps claim one image the app named like the image wins.
export function iconsByImage(apps: { id: string; image?: string; icon?: string }[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const a of apps) {
    if (!a.image || !a.icon) continue;
    const named = a.image.split('/').pop() === a.id;
    if (!out[a.image] || named) out[a.image] = a.icon;
  }
  return out;
}

// serviceIcon is the icon of the app a service runs, by its image without
// tag or digest; "" for an image no catalog knows (a database).
export function serviceIcon(byImage: Record<string, string>, image?: string): string {
  if (!image) return '';
  let ref = image.split('@')[0];
  const colon = ref.lastIndexOf(':');
  if (colon > ref.lastIndexOf('/')) ref = ref.slice(0, colon);
  return byImage[ref] || '';
}

// Deterministic tile colour for stacks with no catalog logo (GNOME palette).
const TILE = ['#3584e4', '#9141ac', '#c061cb', '#e5a50a', '#26a269', '#c01c28', '#1c71d8', '#986a44'];
export const tile = (n: string) => TILE[[...n].reduce((a, c) => a + c.charCodeAt(0), 0) % TILE.length];
export const initial = (n: string) => (n[0] ?? '?').toUpperCase();
