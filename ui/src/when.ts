// Every date fjord shows is in the format chosen in Settings, ISO 8601
// unless changed (2026-11-24); times are 24-hour (07:11), all in the
// browser's time zone. Never the browser's locale format, which shows the
// same moment differently to each viewer.

import { showDate } from './dateFormat';
import { showTime, showDateTime } from './timeFormat';

type When = string | number | Date;

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

/** 07:11, 7:11 AM, or 07:11-05:00 in the chosen time format. */
export function isoTime(t: When, seconds = false): string {
  const d = at(t);
  return d ? showTime(d, seconds) : '';
}

/** Date and time in the chosen formats. */
export function isoDateTime(t: When, seconds = false): string {
  const d = at(t);
  return d ? showDateTime(isoDate(d), d, seconds) : '';
}
