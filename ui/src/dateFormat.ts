// Settings > Date format: every strictly ISO 8601 way of writing a date
// (the Approved entries of the ahze/yyyymmdd catalog, a submodule pinned
// at a commit: ui/vendor/yyyymmdd), then a few that are not ISO 8601 at
// all. Every date fjord shows is written in the chosen one (when.ts);
// times stay 24-hour. The choice is remembered per browser, like the
// language.

import catalog from '../vendor/yyyymmdd/date-standards.json';
import { currentLocale } from './locale';

export type DateFormat = {
  id: string;
  iso: boolean;
  standard: string;
  description: string;
  format: string;
  ref?: string;
  url?: string;
};

const ISO: DateFormat[] = catalog.formats
  .filter((f) => f.verdict === 'Approved')
  .map((f) => ({
    id: f.id,
    iso: true,
    standard: f.standard,
    description: f.description,
    format: f.format,
    ref: f.reference.label,
    url: f.reference.url,
  }));

const OTHER: DateFormat[] = [
  { id: 'month-first', iso: false, standard: 'Month first', description: 'Month, day, year.', format: 'MM/DD/YYYY' },
  { id: 'day-first', iso: false, standard: 'Day first', description: 'Day, month, year.', format: 'DD/MM/YYYY' },
  { id: 'written-out', iso: false, standard: 'Written out', description: 'The month by name.', format: 'MMMM Do, YYYY' },
];

export const FORMATS: readonly DateFormat[] = [...ISO, ...OTHER];

export const DEFAULT_FORMAT = 'iso-8601-extended-calendar';

const KEY = 'fjord-date-format';

const known = (id: unknown): id is string => FORMATS.some((f) => f.id === id);

/** The chosen format's id, or the default. */
export function currentDateFormat(): string {
  try {
    const v = localStorage.getItem(KEY);
    return known(v) ? v : DEFAULT_FORMAT;
  } catch {
    return DEFAULT_FORMAT;
  }
}

let active: DateFormat | undefined;

/** Remember the choice; the default leaves nothing stored. */
export function setDateFormat(id: string) {
  if (!known(id)) return;
  active = formatById(id);
  try {
    if (id === DEFAULT_FORMAT) localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, id);
  } catch {
    // private mode: the choice lasts for this page only
  }
}

export function formatById(id: string): DateFormat {
  return FORMATS.find((f) => f.id === id) ?? FORMATS.find((f) => f.id === DEFAULT_FORMAT)!;
}

const pad = (n: number, w = 2) => String(n).padStart(w, '0');
const DAY = 86400000;

// The ISO week-numbering year and week: the week's Thursday decides.
function isoWeek(y: number, m: number, d: number): [number, number, number] {
  const t = Date.UTC(y, m - 1, d);
  const dow = ((new Date(t).getUTCDay() + 6) % 7) + 1;
  const thu = new Date(t + (4 - dow) * DAY);
  const wy = thu.getUTCFullYear();
  return [wy, Math.floor((thu.getTime() - Date.UTC(wy, 0, 1)) / DAY / 7) + 1, dow];
}

function writtenOut(y: number, m: number, d: number, locale: string): string {
  const month = new Intl.DateTimeFormat(locale, { month: 'long', timeZone: 'UTC' }).format(Date.UTC(y, m - 1, d));
  if (locale === 'fr') return `${d === 1 ? '1er' : d} ${month} ${y}`;
  const th = d % 100 >= 11 && d % 100 <= 13 ? 'th' : ({ 1: 'st', 2: 'nd', 3: 'rd' } as Record<number, string>)[d % 10] ?? 'th';
  return `${month} ${d}${th}, ${y}`;
}

/** Year, month (1-12) and day written in a format; an unknown format is YYYY-MM-DD. */
export function formatDate(y: number, m: number, d: number, format: string, locale = 'en'): string {
  const doy = pad((Date.UTC(y, m - 1, d) - Date.UTC(y, 0, 1)) / DAY + 1, 3);
  switch (format) {
    case 'YYYYMMDD':
      return `${y}${pad(m)}${pad(d)}`;
    case 'YYYY-DDD':
      return `${y}-${doy}`;
    case 'YYYYDDD':
      return `${y}${doy}`;
    case 'YYYY-Www-D': {
      const [wy, w, dow] = isoWeek(y, m, d);
      return `${wy}-W${pad(w)}-${dow}`;
    }
    case 'YYYYWwwD': {
      const [wy, w, dow] = isoWeek(y, m, d);
      return `${wy}W${pad(w)}${dow}`;
    }
    case 'MM/DD/YYYY':
      return `${pad(m)}/${pad(d)}/${y}`;
    case 'DD/MM/YYYY':
      return `${pad(d)}/${pad(m)}/${y}`;
    case 'MMMM Do, YYYY':
      return writtenOut(y, m, d, locale);
    default:
      return `${y}-${pad(m)}-${pad(d)}`;
  }
}

let locale: string | undefined;

/** A date in the chosen format and the page's language. */
export function showDate(y: number, m: number, d: number): string {
  active ??= formatById(currentDateFormat());
  locale ??= currentLocale();
  return formatDate(y, m, d, active.format, locale);
}
