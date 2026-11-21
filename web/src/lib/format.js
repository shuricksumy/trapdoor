// Display helpers. Bytes use decimal units (what ISPs and cloud bills use).

export function bytes(n) {
  if (!n) return '0 B';
  const u = ['B', 'kB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  let v = n;
  while (v >= 1000 && i < u.length - 1) {
    v /= 1000;
    i++;
  }
  const num = v >= 100 || i === 0 ? String(Math.round(v)) : v.toFixed(1).replace(/\.0$/, '');
  return `${num} ${u[i]}`;
}

export function duration(sec) {
  if (sec < 60) return `${Math.max(0, Math.round(sec))} s`;
  const m = Math.floor(sec / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h} h ${m % 60} min`;
  return `${Math.floor(h / 24)} d ${h % 24} h`;
}

const dateFmt = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' });
const dateTimeFmt = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
const timeFmt = new Intl.DateTimeFormat(undefined, { timeStyle: 'short' });

export const date = (s) => (s ? dateFmt.format(new Date(s)) : '');
export const dateTime = (s) => (s ? dateTimeFmt.format(new Date(s)) : '');
export const time = (s) => (s ? timeFmt.format(new Date(s)) : '');

/** "in 12 days", "3 days ago" */
export function relative(s, now = Date.now()) {
  const ms = new Date(s) - now;
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
  // within a day in hours (13 hours ago, not "yesterday"), within an hour in minutes
  if (Math.abs(ms) < 3600000) return rtf.format(Math.round(ms / 60000), 'minute');
  if (Math.abs(ms) < 86400000) return rtf.format(Math.round(ms / 3600000), 'hour');
  const days = Math.round(ms / 86400000);
  if (Math.abs(days) < 60) return rtf.format(days, 'day');
  return rtf.format(Math.round(days / 30), 'month');
}

/** Effective state for display: valid / expiring / expired / revoked. */
export function certState(c, warnDays = 30) {
  if (c.status === 'revoked') return { key: 'revoked', label: 'Revoked', tone: 'bad' };
  if (c.status === 'expired') return { key: 'expired', label: 'Expired', tone: 'bad' };
  const days = (new Date(c.not_after) - Date.now()) / 86400000;
  if (days < warnDays) return { key: 'expiring', label: 'Expires soon', tone: 'warn' };
  return { key: 'valid', label: 'Valid', tone: 'ok' };
}

/** YYYY-MM-DD for <input type="date"> in local time. */
export function isoDate(d) {
  const z = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${z(d.getMonth() + 1)}-${z(d.getDate())}`;
}
