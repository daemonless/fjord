// Every date and time fjord shows is ISO 8601, 24-hour, in the browser's
// time zone: 2026-11-24, 07:11, 2026-11-24 07:11. Never the browser's
// locale format, which shows the same moment differently to each viewer.

type When = string | number | Date;

const pad = (n: number) => String(n).padStart(2, '0');

function at(t: When): Date | undefined {
  const d = t instanceof Date ? t : new Date(t);
  return isNaN(d.getTime()) ? undefined : d;
}

/** 2026-11-24; utc for dates that are days, not moments (a build day). */
export function isoDate(t: When, utc = false): string {
  const d = at(t);
  if (!d) return '';
  return utc
    ? `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`
    : `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** 07:11, or 07:11:05 with seconds. */
export function isoTime(t: When, seconds = false): string {
  const d = at(t);
  if (!d) return '';
  return `${pad(d.getHours())}:${pad(d.getMinutes())}${seconds ? `:${pad(d.getSeconds())}` : ''}`;
}

/** 2026-11-24 07:11, or 2026-11-24 07:11:05 with seconds. */
export function isoDateTime(t: When, seconds = false): string {
  const d = at(t);
  return d ? `${isoDate(d)} ${isoTime(d, seconds)}` : '';
}
