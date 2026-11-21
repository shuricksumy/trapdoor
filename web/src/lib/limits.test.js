import { test } from 'node:test';
import assert from 'node:assert/strict';
import { serverForm, fromServerForm, money, speedText, speedPair, quotaText, toKbit, fromKbit, toMB, usagePct, ownGroups, toForm, fromForm, overText, summary, resetsIn } from './limits.js';

test('speeds and sizes in words', () => {
  assert.equal(speedText(0), 'unlimited');
  assert.equal(speedText(512), '512 kbit/s');
  assert.equal(speedText(1000), '1 Mbit/s');
  assert.equal(speedText(2500), '2.5 Mbit/s');
  assert.equal(speedPair(1000, 1000), '1 Mbit/s');
  assert.equal(speedPair(50000, 10000), '50 Mbit/s ↓ · 10 Mbit/s ↑');
  assert.equal(quotaText(0), 'no limit');
  assert.equal(quotaText(300), '300 MB');
  assert.equal(quotaText(20000), '20 GB');
  assert.equal(quotaText(1500), '1.5 GB');
});

test('typed values', () => {
  assert.equal(toKbit(''), 0);
  assert.equal(toKbit(' 1.5 '), 1500);
  assert.equal(toKbit('0,5'), 500);
  assert.ok(Number.isNaN(toKbit('fast')));
  assert.ok(Number.isNaN(toKbit('-1')));
  assert.equal(fromKbit(0), '');
  assert.equal(fromKbit(2500), '2.5');
  assert.equal(toMB('20'), 20000);
  assert.equal(toMB(''), 0);
});

test('usage share', () => {
  assert.equal(usagePct(5e9, 0), null);
  assert.equal(usagePct(9e8, 1000), 90);
  assert.equal(usagePct(1.2e9, 1000), 120);
});

test('own groups and the form', () => {
  assert.deepEqual(ownGroups({ down: 0, up: null, quota_mb: null }), { speed: true, usage: false, over: false, prio: false });
  assert.deepEqual(ownGroups({ priority: 'high' }), { speed: false, usage: false, over: false, prio: true });
  assert.deepEqual(ownGroups(), { speed: false, usage: false, over: false, prio: false });
  const f = toForm({ down: 50000, up: 0, quota_mb: 20000, period: 'week', slow_down: 1000, slow_up: 1000, block: false });
  assert.deepEqual(f, { down: '50', up: '', quota: '20', period: 'week', slow_down: '1', slow_up: '1', block: false, priority: 'normal' });
  // only the usage group of its own
  assert.deepEqual(fromForm(f, { speed: false, usage: true, over: false, prio: false }), { down: null, up: null, quota_mb: 20000, period: 'week', slow_down: null, slow_up: null, block: null, priority: null });
  // the server's default: everything
  assert.deepEqual(fromForm({ ...f, priority: 'low' }), { down: 50000, up: 0, quota_mb: 20000, period: 'week', slow_down: 1000, slow_up: 1000, block: false, priority: 'low' });
  assert.throws(() => fromForm({ ...f, down: 'x' }), /Download speed/);
  assert.throws(() => fromForm({ ...f, slow_up: '' }), /give both/);
  assert.equal(fromForm({ ...f, slow_up: '', block: true }).block, true); // blocked: no slowed speed needed
});

test('in words, tiles, resets', () => {
  assert.equal(overText({ quota_mb: 0 }), 'nothing (no usage limit)');
  assert.equal(overText({ quota_mb: 1, block: true }), 'blocked until the period ends');
  assert.equal(overText({ quota_mb: 1, slow_down: 1000, slow_up: 1000 }), 'slowed to 1 Mbit/s');
  const rows = [
    { state: 'slowed', used: 2e9, limit: { quota_mb: 1000 } },
    { state: 'capped', used: 8.5e8, limit: { quota_mb: 1000 } },
    { state: 'full', used: 5e9, limit: { quota_mb: 0 } },
    { state: 'blocked', used: 1e6, limit: { quota_mb: 1 } },
  ];
  assert.deepEqual(summary(rows), { slowed: 2, near: 1, withLimit: 3 });
  const now = Date.UTC(2026, 10, 20, 12);
  assert.equal(resetsIn(new Date(Date.UTC(2026, 11, 1)), now), 'in 10 days');
  assert.equal(resetsIn(new Date(now + 5 * 3600e3), now), 'in 5 hours');
  assert.equal(resetsIn(new Date(now + 30 * 60e3), now), 'in 30 minutes');
});

test('server settings and money', () => {
  const f = serverForm({ link_down: 100000, link_up: 0, alarm_hour_mb: 2000, price_out: 0.09, currency: '€' });
  assert.deepEqual(f, { link_down: '100', link_up: '', alarm_hour: '2', alarm_day: '', alarm_month: '', price_in: '', price_out: '0.09', currency: '€' });
  assert.deepEqual(fromServerForm({ ...f, price_in: '0,05', currency: ' ' }), {
    link_down: 100000, link_up: 0, alarm_hour_mb: 2000, alarm_day_mb: 0, alarm_month_mb: 0, price_in: 0.05, price_out: 0.09, currency: '$',
  });
  assert.throws(() => fromServerForm({ ...f, price_in: 'free' }), /Price in/);
  assert.throws(() => fromServerForm({ ...f, alarm_day: 'x' }), /Alarm per day/);
  assert.equal(money(1.143), '$1.14');
  assert.equal(money(123.4, '€'), '€123');
  assert.equal(money(2.5, 'UAH'), '2.50 UAH');
});
