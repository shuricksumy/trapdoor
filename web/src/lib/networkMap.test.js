// node --test: the OpenVPN network map.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildMap, covers, normalize, overlaps, parse } from './networkMap.js';

const srv = { subnet: '10.8.0.0/24', server_ip: '10.8.0.1', pushes_full_tunnel: false, routes: ['192.168.1.0/24'] };
const clients = [
  { name: 'home-router', static_ip: '10.8.0.10', full_tunnel: false, routes: [], lan: ['192.168.1.0/24'], named: [] },
  { name: 'office-gw', static_ip: '', full_tunnel: false, routes: [], lan: ['192.168.50.0/24'], named: [] },
  { name: 'anna', static_ip: '', full_tunnel: true, routes: ['192.168.1.0/24'], lan: [], named: [] },
  { name: 'bob', static_ip: '', full_tunnel: false, routes: ['172.16.0.0/12'], lan: [], named: [] },
];
const byKey = (m) => Object.fromEntries(m.networks.map((n) => [n.key, n]));

test('parse and covers', () => {
  assert.deepEqual(parse('10.8.0.1'), [0x0a080001, 32]);
  assert.equal(parse('300.1.1.1/24'), null);
  assert.equal(parse('10.0.0.0/33'), null);
  assert.ok(covers('192.168.0.0/16', '192.168.1.0/24'));
  assert.ok(covers('192.168.1.0/24', '192.168.1.0/24'));
  assert.ok(covers('0.0.0.0/0', '10.1.2.3'));
  assert.ok(!covers('192.168.1.0/24', '192.168.0.0/16'));
  assert.ok(!covers('10.0.0.0/8', 'nonsense'));
});

test('LANs behind clients, networks through the server, and who reaches them', () => {
  const m = buildMap({ srv, clients, pushed: [], online: new Set(['anna']) });
  const n = byKey(m);
  assert.equal(n['lan:192.168.1.0/24'].via, 'home-router');
  assert.deepEqual(n['lan:192.168.1.0/24'].reachedBy, ['anna']);
  assert.equal(n['lan:192.168.1.0/24'].ok, true);
  // bob's route goes through the server; nobody has it behind them
  assert.equal(n['srv:172.16.0.0/12'].via, '');
  assert.deepEqual(n['srv:172.16.0.0/12'].reachedBy, ['bob']);
  // routers first, then online, then by name
  assert.deepEqual(m.clients.map((c) => c.name), ['home-router', 'office-gw', 'anna', 'bob']);
  assert.deepEqual(m.clients[0].router, ['192.168.1.0/24']);
  assert.equal(m.clients.find((c) => c.name === 'anna').online, true);
  assert.deepEqual(m.internet, ['anna']);
});

test('a LAN behind a client without a server route is a problem with its CIDR', () => {
  const m = buildMap({ srv, clients });
  const office = byKey(m)['lan:192.168.50.0/24'];
  assert.equal(office.ok, false);
  assert.deepEqual(office.missingRoute, ['192.168.50.0/24']);
  assert.deepEqual(
    m.problems.filter((p) => p.network === 'lan:192.168.50.0/24').map((p) => p.kind),
    ['route', 'unreached'],
  );
  // a wider server route covers it
  const m2 = buildMap({ srv: { ...srv, routes: ['192.168.0.0/16'] }, clients });
  assert.equal(byKey(m2)['lan:192.168.50.0/24'].ok, true);
});

test('pushed routes reach every client; full tunnel from the server', () => {
  const m = buildMap({ srv: { ...srv, pushes_full_tunnel: true }, clients, pushed: ['192.168.50.0/24'] });
  assert.equal(byKey(m)['lan:192.168.50.0/24'].reachedBy, 'all');
  assert.ok(!m.problems.some((p) => p.kind === 'unreached' && p.network === 'lan:192.168.50.0/24'));
  assert.equal(m.internet, 'all');
});

test('named networks are one node; their CIDRs are not listed again', () => {
  const nets = [{ name: 'Office LAN', networks: ['192.168.50.0/24', '192.168.51.0/24'], via: 'office-gw', clients: ['bob'] }];
  const cs = clients.map((c) => (c.name === 'office-gw' ? { ...c, lan: [] } : c));
  const m = buildMap({ srv: { ...srv, routes: ['192.168.1.0/24', '192.168.50.0/24'] }, clients: cs, nets });
  const n = byKey(m);
  assert.ok(n['named:Office LAN']);
  assert.ok(!n['lan:192.168.50.0/24']);
  assert.deepEqual(n['named:Office LAN'].reachedBy, ['bob']);
  assert.deepEqual(n['named:Office LAN'].missingRoute, ['192.168.51.0/24']);
  assert.deepEqual(m.clients.find((c) => c.name === 'office-gw').router, ['Office LAN']);
});

test('a named network through the server (no via) needs no route', () => {
  const nets = [{ name: 'Server LAN', networks: ['10.20.0.0/16'], via: '', clients: ['anna'] }];
  const m = buildMap({ srv, clients, nets });
  const n = byKey(m)['named:Server LAN'];
  assert.equal(n.ok, true);
  assert.deepEqual(n.reachedBy, ['anna']);
  assert.ok(!m.problems.some((p) => p.network === 'named:Server LAN'));
});

test('a server route that no client has behind it is reported', () => {
  const m = buildMap({ srv: { ...srv, routes: ['192.168.1.0/24', '10.99.0.0/16'] }, clients });
  assert.deepEqual(m.problems.filter((p) => p.kind === 'orphan'), [{ kind: 'orphan', cidr: '10.99.0.0/16' }]);
});

test('no clients, no networks', () => {
  const m = buildMap({ srv: { ...srv, routes: [] }, clients: [] });
  assert.deepEqual(m, { clients: [], networks: [], internet: [], problems: [] });
});

test('normalize and overlaps', () => {
  assert.equal(normalize('192.168.1.5/24'), '192.168.1.0/24');
  assert.equal(normalize(' 10.1.2.3 '), '10.1.2.3/32');
  assert.equal(normalize('0.0.0.0/0'), '0.0.0.0/0');
  assert.equal(normalize('10.0.0.0/8x'), null);
  assert.ok(overlaps('10.8.0.0/24', '10.8.0.0/16'));
  assert.ok(!overlaps('10.8.0.0/24', '10.9.0.0/24'));
});

test('client-to-client: a LAN without a server route is fine for clients, noted for the server', () => {
  const m = buildMap({ srv, clients, c2c: true });
  const office = byKey(m)['lan:192.168.50.0/24'];
  assert.equal(office.ok, true);
  assert.deepEqual(office.missingRoute, []);
  assert.deepEqual(office.serverOnly, ['192.168.50.0/24']);
  assert.ok(!m.problems.some((p) => p.kind === 'route'));
  // nobody reaching it is still worth saying
  assert.ok(m.problems.some((p) => p.kind === 'unreached' && p.network === 'lan:192.168.50.0/24'));
});
