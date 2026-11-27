// Every date fjord shows is in the format chosen in Settings, ISO 8601
// unless changed (2026-11-24); times are 24-hour (07:11), all in the
// browser's time zone. Never the browser's locale format, which shows the
// same moment differently to each viewer.

import { showDate } from './dateFormat';

type When = string | number | Date;

const pad = (n: number) => String(n).padStart(2, '0');

function at(t: When): Date | undefined {
  const d = t instanceof Date ? t : new Date(t);
  return isNaN(d.getTime()) ? undefined : d;
}

/** 2026-11-24, or the chosen format; utc for dates that are days, not moments (a build day). */
export function isoDate(t: When, utc = false): string {
  const d = at(t);
  if (!d) return '';
  return utc
    ? showDate(d.getUTCFullYear(), d.getUTCMonth() + 1, d.getUTCDate())
    : showDate(d.getFullYear(), d.getMonth() + 1, d.getDate());
}

/** 07:11, or 07:11:05 with seconds. */
export function isoTime(t: When, seconds = false): string {
  const d = at(t);
  if (!d) return '';
  return `${pad(d.getHours())}:${pad(d.getMinutes())}${seconds ? `:${pad(d.getSeconds())}` : ''}`;
}

/** 2026-11-24 07:11, or 2026-11-24 07:11:05 with seconds; the date in the chosen format. */
export function isoDateTime(t: When, seconds = false): string {
  const d = at(t);
  return d ? `${isoDate(d)} ${isoTime(d, seconds)}` : '';
}
