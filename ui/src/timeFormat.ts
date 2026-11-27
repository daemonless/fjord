// Settings > Time format: 24-hour (default), 12-hour, or ISO 8601 with
// local UTC offset. Remembered per browser in localStorage, like Date format.

export type TimeFormat = '24' | '12' | 'iso';

export const DEFAULT_TIME_FORMAT: TimeFormat = '24';

const KEY = 'fjord-time-format';

const known = (id: unknown): id is TimeFormat => id === '24' || id === '12' || id === 'iso';

let active: TimeFormat | undefined;

export function currentTimeFormat(): TimeFormat {
  try {
    const v = localStorage.getItem(KEY);
    return known(v) ? v : DEFAULT_TIME_FORMAT;
  } catch {
    return DEFAULT_TIME_FORMAT;
  }
}

export function setTimeFormat(id: TimeFormat) {
  if (!known(id)) return;
  active = id;
  try {
    if (id === DEFAULT_TIME_FORMAT) localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, id);
  } catch {
    // private mode: lasts for this page only
  }
}

function chosen(): TimeFormat {
  if (!active) active = currentTimeFormat();
  return active;
}

const pad = (n: number) => String(n).padStart(2, '0');

function tzOffset(d: Date): string {
  const off = -d.getTimezoneOffset();
  if (off === 0) return 'Z';
  const sign = off >= 0 ? '+' : '-';
  const abs = Math.abs(off);
  return `${sign}${pad(Math.floor(abs / 60))}:${pad(abs % 60)}`;
}

export function formatTime(d: Date, format: TimeFormat, seconds = false): string {
  const h = d.getHours();
  const m = pad(d.getMinutes());
  const s = seconds ? `:${pad(d.getSeconds())}` : '';
  switch (format) {
    case '12': {
      const h12 = h % 12 || 12;
      const ampm = h < 12 ? 'AM' : 'PM';
      return `${h12}:${m}${s} ${ampm}`;
    }
    case 'iso':
      return `${pad(h)}:${m}${s}${tzOffset(d)}`;
    default:
      return `${pad(h)}:${m}${s}`;
  }
}

export function showTime(d: Date, seconds = false): string {
  return formatTime(d, chosen(), seconds);
}

export function showDateTime(dateStr: string, d: Date, seconds = false): string {
  const fmt = chosen();
  const sep = fmt === 'iso' ? 'T' : ' ';
  return `${dateStr}${sep}${formatTime(d, fmt, seconds)}`;
}
