// The Limits page: speeds (kbit/s in the API, Mbit/s on screen), usage
// limits (MB in the API, GB on screen, decimal like ISPs) and the three
// groups a client can set for itself instead of the server's default.

/** 0 → "unlimited", 1000 → "1 Mbit/s", 2500 → "2.5 Mbit/s", 512 → "512 kbit/s". */
export function speedText(kbit) {
  if (!kbit) return 'unlimited';
  if (kbit < 1000) return `${kbit} kbit/s`;
  return `${trim(kbit / 1000)} Mbit/s`;
}

/** Download and upload: one value when they are the same. */
export function speedPair(down, up) {
  if (down === up) return speedText(down);
  return `${speedText(down)} ↓ · ${speedText(up)} ↑`;
}

/** 1000 → "1 GB", 1500 → "1.5 GB", 300 → "300 MB", 0 → "no limit". */
export function quotaText(mb) {
  if (!mb) return 'no limit';
  if (mb < 1000) return `${mb} MB`;
  return `${trim(mb / 1000)} GB`;
}

const trim = (v) => String(Math.round(v * 100) / 100);

/** Mbit/s as typed → kbit/s: "" = 0 (unlimited); NaN when not a number ≥ 0. */
export function toKbit(s) {
  const t = String(s ?? '').trim().replace(',', '.');
  if (t === '') return 0;
  const v = Number(t);
  return Number.isFinite(v) && v >= 0 ? Math.round(v * 1000) : NaN;
}

/** kbit/s → Mbit/s for an input ("" when unlimited). */
export const fromKbit = (k) => (k ? trim(k / 1000) : '');

/** GB as typed → MB: "" = 0 (no limit); NaN when not a number ≥ 0. */
export function toMB(s) {
  const t = String(s ?? '').trim().replace(',', '.');
  if (t === '') return 0;
  const v = Number(t);
  return Number.isFinite(v) && v >= 0 ? Math.round(v * 1000) : NaN;
}

export const fromMB = (mb) => (mb ? trim(mb / 1000) : '');

/** Share of the usage limit used, 0–100+ (null without a limit). */
export function usagePct(used, quotaMB) {
  if (!quotaMB) return null;
  return Math.floor((used * 100) / (quotaMB * 1e6));
}

export const periodText = { day: 'a day', week: 'a week', month: 'a month' };

/** The fields of each group a client can set for itself. */
export const groups = {
  speed: ['down', 'up'],
  usage: ['quota_mb', 'period'],
  over: ['slow_down', 'slow_up', 'block'],
  prio: ['priority'],
};

/** Which groups a client sets itself (any field of the group). */
export function ownGroups(own = {}) {
  const out = {};
  for (const [g, fields] of Object.entries(groups)) out[g] = fields.some((f) => own[f] != null);
  return out;
}

/** The form of a limit (values on screen). */
export function toForm(limit) {
  return {
    down: fromKbit(limit.down), up: fromKbit(limit.up),
    quota: fromMB(limit.quota_mb), period: limit.period || 'month',
    slow_down: fromKbit(limit.slow_down), slow_up: fromKbit(limit.slow_up), block: !!limit.block,
    priority: limit.priority || 'normal',
  };
}

/**
 * The LimitSet to send: the groups in `own` (all for the server's default)
 * from the form, the others null (follow the default). Throws with a
 * message for a bad value.
 */
export function fromForm(form, own = { speed: true, usage: true, over: true, prio: true }) {
  const out = { down: null, up: null, quota_mb: null, period: null, slow_down: null, slow_up: null, block: null, priority: null };
  const num = (v, conv, what) => {
    const n = conv(v);
    if (Number.isNaN(n)) throw new Error(`${what}: a number, or empty`);
    return n;
  };
  if (own.speed) {
    out.down = num(form.down, toKbit, 'Download speed');
    out.up = num(form.up, toKbit, 'Upload speed');
  }
  if (own.usage) {
    out.quota_mb = num(form.quota, toMB, 'Usage limit');
    out.period = form.period || 'month';
  }
  if (own.over) {
    out.block = !!form.block;
    out.slow_down = num(form.slow_down, toKbit, 'Slowed download speed');
    out.slow_up = num(form.slow_up, toKbit, 'Slowed upload speed');
    if (!out.block && (!out.slow_down || !out.slow_up)) throw new Error('Slowed speed: give both, or block instead');
  }
  if (own.prio) out.priority = form.priority || 'normal';
  return out;
}

/** What happens at the limit, in words. */
export function overText(limit) {
  if (!limit.quota_mb) return 'nothing (no usage limit)';
  return limit.block ? 'blocked until the period ends' : `slowed to ${speedPair(limit.slow_down, limit.slow_up)}`;
}

/** Numbers for the tiles. */
export function summary(rows) {
  let slowed = 0, near = 0, withLimit = 0;
  for (const r of rows) {
    if (r.state === 'slowed' || r.state === 'blocked') slowed++;
    const p = usagePct(r.used, r.limit.quota_mb);
    if (p != null) {
      withLimit++;
      if (p >= 80 && p < 100) near++;
    }
  }
  return { slowed, near, withLimit };
}

/** "in 3 days", "in 5 hours", "in 20 minutes". */
export function resetsIn(end, now = Date.now()) {
  const s = Math.max(0, (new Date(end).getTime() - now) / 1000);
  if (s >= 2 * 86400) return `in ${Math.floor(s / 86400)} days`;
  if (s >= 2 * 3600) return `in ${Math.floor(s / 3600)} hours`;
  if (s >= 120) return `in ${Math.floor(s / 60)} minutes`;
  return 'in a minute';
}

/** The server settings form (link speeds, alarms, prices) on screen. */
export function serverForm(ss = {}) {
  return {
    link_down: fromKbit(ss.link_down), link_up: fromKbit(ss.link_up),
    alarm_hour: fromMB(ss.alarm_hour_mb), alarm_day: fromMB(ss.alarm_day_mb), alarm_month: fromMB(ss.alarm_month_mb),
    price_in: ss.price_in ? String(ss.price_in) : '', price_out: ss.price_out ? String(ss.price_out) : '',
    currency: ss.currency || '$',
  };
}

/** The server settings to send; throws with a message for a bad value. */
export function fromServerForm(f) {
  const num = (v, conv, what) => {
    const n = conv(v);
    if (Number.isNaN(n)) throw new Error(`${what}: a number, or empty`);
    return n;
  };
  const price = (v, what) => {
    const t = String(v ?? '').trim().replace(',', '.');
    if (t === '') return 0;
    const n = Number(t);
    if (!Number.isFinite(n) || n < 0) throw new Error(`${what}: a price, or empty`);
    return n;
  };
  return {
    link_down: num(f.link_down, toKbit, 'Link download'), link_up: num(f.link_up, toKbit, 'Link upload'),
    alarm_hour_mb: num(f.alarm_hour, toMB, 'Alarm per hour'), alarm_day_mb: num(f.alarm_day, toMB, 'Alarm per day'),
    alarm_month_mb: num(f.alarm_month, toMB, 'Alarm per month'),
    price_in: price(f.price_in, 'Price in'), price_out: price(f.price_out, 'Price out'),
    currency: String(f.currency ?? '').trim() || '$',
  };
}

/** 1.143 → "$1.14", with the currency before short symbols and after codes. */
export function money(v, currency = '$') {
  const n = (v ?? 0) < 10 ? (v ?? 0).toFixed(2) : (v ?? 0).toFixed(0);
  return currency.length > 1 ? `${n} ${currency}` : `${currency}${n}`;
}

/** How fast a VPN type usually goes, for the hints next to speeds. */
export const typeSpeed = {
  openvpn: 'OpenVPN runs as one process per server: usually 100–500 Mbit/s in total, depending on the CPU and cipher (more with the DCO kernel module).',
  wireguard: 'WireGuard runs in the kernel: up to 1 Gbit/s and more; usually the server\'s network is the limit.',
  amneziawg: 'AmneziaWG runs in userspace (amneziawg-go): usually a few hundred Mbit/s, depending on the CPU.',
};
