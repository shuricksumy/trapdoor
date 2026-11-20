// The uptime view's arithmetic, kept apart so it can be tested.

/** Seconds a bucket covers so far: the whole step, or up to now for the current one. */
export function bucketSpan(start, step, now, next) {
  const s = new Date(start).getTime();
  const end = next ? new Date(next).getTime() : s + (step === 'day' ? 86400e3 : 3600e3);
  return Math.max(0, (Math.min(end, now.getTime()) - s) / 1000);
}

/** A status bar segment: 'future', 'none', 'part' or 'full' (online almost all of it). */
export function level(seconds, span) {
  if (span <= 0) return 'future';
  if (!seconds) return 'none';
  return seconds / span >= 0.95 ? 'full' : 'part';
}

/** Share of the period so far the client was online, 0-100 with one decimal. */
export function uptimePct(onlineSec, report, now) {
  const from = new Date(report.from).getTime();
  const to = Math.min(new Date(report.to).getTime(), now.getTime());
  const span = (to - from) / 1000;
  if (span <= 0) return 0;
  return Math.round(Math.min(100, (onlineSec / span) * 100) * 10) / 10;
}

/** Order: online first, then the most recently seen, then never seen; by name within. */
export function sortClients(rows, label = (n) => n) {
  const seen = (r) => (r.online ? Infinity : r.last_seen ? new Date(r.last_seen).getTime() : -Infinity);
  return [...rows].sort((a, b) => seen(b) - seen(a) || label(a.name).localeCompare(label(b.name)));
}

/**
 * A month as calendar weeks (Monday first): cells of { day, i } where i is
 * the index into the month's day buckets, null outside the month.
 */
export function monthGrid(year, month) {
  const first = new Date(Date.UTC(year, month - 1, 1));
  const days = new Date(Date.UTC(year, month, 0)).getUTCDate();
  const lead = (first.getUTCDay() + 6) % 7; // Monday = 0
  const cells = [];
  for (let k = 0; k < lead; k++) cells.push(null);
  for (let d = 1; d <= days; d++) cells.push({ day: d, i: d - 1 });
  while (cells.length % 7) cells.push(null);
  const weeks = [];
  for (let k = 0; k < cells.length; k += 7) weeks.push(cells.slice(k, k + 7));
  return weeks;
}

/** YYYY-MM-DD in local time. */
const ymd = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;

/** The quick picks of the Uptime page: { from, to } as YYYY-MM-DD, both days included. */
export function preset(key, now = new Date()) {
  const y = now.getFullYear();
  const m = now.getMonth();
  const today = new Date(y, m, now.getDate());
  const back = (days) => new Date(y, m, now.getDate() - days);
  switch (key) {
    case 'today':
      return { from: ymd(today), to: ymd(today) };
    case '7d':
      return { from: ymd(back(6)), to: ymd(today) };
    case '30d':
      return { from: ymd(back(29)), to: ymd(today) };
    case 'this_month':
      return { from: ymd(new Date(y, m, 1)), to: ymd(today) };
    case 'last_month':
      return { from: ymd(new Date(y, m - 1, 1)), to: ymd(new Date(y, m, 0)) };
    case 'this_year':
      return { from: ymd(new Date(y, 0, 1)), to: ymd(today) };
    default:
      return null;
  }
}

/** Which quick pick a range is, or '' for a custom one. */
export function presetOf(from, to, now = new Date()) {
  for (const k of ['today', '7d', '30d', 'this_month', 'last_month', 'this_year']) {
    const p = preset(k, now);
    if (p.from === from && p.to === to) return k;
  }
  return '';
}

/**
 * The page buttons to show: the first, the last, and two around the current
 * one, with '…' for the gaps. Pages are numbered from 1.
 */
export function pageList(current, pages) {
  if (pages <= 7) return Array.from({ length: pages }, (_, i) => i + 1);
  const out = [1];
  // a fixed width (nine items for long lists), so the buttons do not jump
  const lo = Math.max(2, Math.min(current - 2, pages - 6));
  const hi = Math.min(pages - 1, Math.max(current + 2, 7));
  if (lo > 2) out.push('…');
  for (let p = lo; p <= hi; p++) out.push(p);
  if (hi < pages - 1) out.push('…');
  out.push(pages);
  return out;
}
