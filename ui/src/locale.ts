// Page language. The catalogs load before mount and the strings are not
// reactive (legacy components, see wuchale.config.js), so a change reloads.
//
// Storage holds only an explicit choice; with none, the browser's language
// decides, as the theme follows the OS.

export const LOCALES = [
  { id: 'en', name: 'English' },
  { id: 'fr', name: 'Français' },
] as const;
export type Locale = (typeof LOCALES)[number]['id'];

const KEY = 'fjord-locale';
const known = (v: unknown): v is Locale => LOCALES.some((l) => l.id === v);

function stored(): Locale | null {
  try {
    const v = localStorage.getItem(KEY);
    return known(v) ? v : null;
  } catch {
    return null;
  }
}

function browserLocale(): Locale {
  for (const tag of navigator.languages ?? [navigator.language]) {
    const base = tag?.toLowerCase().split('-')[0];
    if (known(base)) return base;
  }
  return 'en';
}

/** The language in effect: the explicit choice if there is one, else the browser's. */
export function currentLocale(): Locale {
  return stored() ?? browserLocale();
}

/** Store the choice and reload, so every string is rendered in it. */
export function setLocale(l: Locale) {
  try {
    if (l === browserLocale()) localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, l);
  } catch {
    // private mode: the choice lasts for this page only
  }
  document.documentElement.lang = l;
  location.reload();
}
