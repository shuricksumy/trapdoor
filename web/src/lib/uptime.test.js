// node --test: the uptime view's arithmetic.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { bucketSpan, level, monthGrid, sortClients, uptimePct } from './uptime.js';

const now = new Date('2026-11-20T12:20:00Z');

test('bucket span: whole step, the current one up to now, the future empty', () => {
  assert.equal(bucketSpan('2026-11-20T10:00:00Z', 'hour', now), 3600);
  assert.equal(bucketSpan('2026-11-20T12:00:00Z', 'hour', now), 20 * 60);
  assert.equal(bucketSpan('2026-11-20T13:00:00Z', 'hour', now), 0);
  assert.equal(bucketSpan('2026-11-19T00:00:00Z', 'day', now), 86400);
  // a day with its next start given (e.g. a DST change)
  assert.equal(bucketSpan('2026-11-19T00:00:00Z', 'day', now, '2026-11-19T23:00:00Z'), 23 * 3600);
});

test('level of a segment: online, offline while the server was up, outage, unknown', async () => {
  const { serverLevel, known } = await import('./uptime.js');
  assert.equal(level(600, 3600, 3600, true), 'part');
  assert.equal(level(3500, 3600, 3600, true), 'full');
  assert.equal(level(1200, 1200, 1200, true), 'full'); // the current hour so far
  assert.equal(level(0, 0, 0, true), 'future');
  assert.equal(level(0, 3600, 3600, true), 'off'); // server up, client not connected
  assert.equal(level(0, 3600, 600, true), 'off'); // up part of the hour
  assert.equal(level(0, 3600, 0, true), 'out'); // the server was down
  assert.equal(level(0, 3600, 20, true), 'out'); // a few seconds of answers do not count
  assert.equal(level(0, 3600, 3600, false), 'none'); // before the server's record
  assert.equal(level(0, 3600), 'none');
  // a client online counts even when the server's record has a gap
  assert.equal(level(900, 3600, 0, true), 'part');
  assert.equal(serverLevel(3600, 3600, true), 'full');
  assert.equal(serverLevel(1800, 3600, true), 'part');
  assert.equal(serverLevel(0, 3600, true), 'out');
  assert.equal(serverLevel(0, 3600, false), 'none');
  assert.equal(known('2026-11-20T10:30:00Z', '2026-11-20T11:00:00Z'), true);
  assert.equal(known('2026-11-20T11:30:00Z', '2026-11-20T11:00:00Z'), false);
  assert.equal(known(null, '2026-11-20T11:00:00Z'), false);
});

test('uptime percent counts the period up to now', () => {
  const report = { from: '2026-11-19T13:00:00Z', to: '2026-11-20T13:00:00Z' };
  // 23 h 20 min so far
  assert.equal(uptimePct(84000, report, now), 100);
  assert.equal(uptimePct(42000, report, now), 50);
  assert.equal(uptimePct(0, report, now), 0);
  assert.equal(uptimePct(10, { from: '2026-11-21T00:00:00Z', to: '2026-11-22T00:00:00Z' }, now), 0);
});

test('clients: online, then last seen, then never; by name', () => {
  const rows = [
    { name: 'zed', online: false, last_seen: null },
    { name: 'bob', online: false, last_seen: '2026-11-18T10:00:00Z' },
    { name: 'anna', online: true, last_seen: '2026-11-20T12:20:00Z' },
    { name: 'carol', online: false, last_seen: '2026-11-20T09:00:00Z' },
    { name: 'amy', online: true },
  ];
  assert.deepEqual(sortClients(rows).map((r) => r.name), ['amy', 'anna', 'carol', 'bob', 'zed']);
});

test('month grid: Monday first, whole weeks', () => {
  const nov = monthGrid(2026, 11); // November 2026 starts on a Sunday
  assert.equal(nov[0].filter(Boolean).length, 1);
  assert.equal(nov[0][6].day, 1);
  assert.ok(nov.every((w) => w.length === 7));
  assert.equal(nov.flat().filter(Boolean).length, 30);
  const feb = monthGrid(2027, 2); // starts on a Monday, 28 days: exactly 4 weeks
  assert.equal(feb.length, 4);
  assert.equal(feb[0][0].day, 1);
});

test('presets: both days included, local dates', async () => {
  const { preset, presetOf } = await import('./uptime.js');
  const now = new Date(2026, 10, 20, 15, 0); // Nov 20, 2026 local
  assert.deepEqual(preset('today', now), { from: '2026-11-20', to: '2026-11-20' });
  assert.deepEqual(preset('7d', now), { from: '2026-11-14', to: '2026-11-20' });
  assert.deepEqual(preset('30d', now), { from: '2026-10-22', to: '2026-11-20' });
  assert.deepEqual(preset('this_month', now), { from: '2026-11-01', to: '2026-11-20' });
  assert.deepEqual(preset('last_month', now), { from: '2026-10-01', to: '2026-10-31' });
  assert.deepEqual(preset('this_year', now), { from: '2026-01-01', to: '2026-11-20' });
  assert.deepEqual(preset('last_month', new Date(2027, 0, 5)), { from: '2026-12-01', to: '2026-12-31' });
  assert.equal(presetOf('2026-11-14', '2026-11-20', now), '7d');
  assert.equal(presetOf('2026-11-02', '2026-11-20', now), '');
});

test('page buttons: first, last, two around the current one', async () => {
  const { pageList } = await import('./uptime.js');
  assert.deepEqual(pageList(1, 1), [1]);
  assert.deepEqual(pageList(2, 5), [1, 2, 3, 4, 5]);
  assert.deepEqual(pageList(1, 20), [1, 2, 3, 4, 5, 6, 7, '…', 20]);
  assert.deepEqual(pageList(10, 20), [1, '…', 8, 9, 10, 11, 12, '…', 20]);
  assert.deepEqual(pageList(20, 20), [1, '…', 14, 15, 16, 17, 18, 19, 20]);
  assert.deepEqual(pageList(4, 8), [1, 2, 3, 4, 5, 6, 7, 8]);
  // the length stays the same while paging through, so the buttons do not jump
  for (let c = 1; c <= 30; c++) assert.equal(pageList(c, 30).length, 9, `page ${c}`);
});

test('stacked parts: short times are kept, not rounded away', async () => {
  const { parts } = await import('./uptime.js');
  const k = (ps) => ps.map((p) => `${p.kind}:${Math.round(p.frac * 3600)}`).join(' ');
  assert.equal(k(parts(60, 3600, 3600, true)), 'on:60 off:3540'); // one minute online
  assert.equal(k(parts(1, 3600, 3600, true)), 'on:1 off:3599'); // one second
  assert.equal(k(parts(3300, 3600, 3600, true)), 'on:3300 off:300'); // five minutes away
  assert.equal(k(parts(0, 3600, 2700, true)), 'off:2700 out:900'); // a 15-minute outage
  assert.equal(k(parts(600, 3600, 0, true)), 'on:600 out:3000'); // online counts as up
  assert.equal(k(parts(0, 3600, 3600, false)), 'none:3600'); // before the server's record
  assert.equal(k(parts(3600, 3600, 3600, true)), 'on:3600');
  assert.deepEqual(parts(0, 0, 0, true), []); // the future
  // the server's own row: up and down
  assert.equal(k(parts(3000, 3600, undefined, true)), 'on:3000 out:600');
});
