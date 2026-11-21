// node --test: the OpenVPN network map.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildMap, buildWGMap, checkNetwork, covers, fold, normalize, overlaps, parse } from './networkMap.js';

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

test('LANs behind two clients that overlap are a problem', () => {
  const cs = [...clients, { name: 'cabin', static_ip: '', full_tunnel: false, routes: [], lan: ['192.168.1.128/25'], named: [] }];
  const m = buildMap({ srv, clients: cs });
  const o = m.problems.filter((p) => p.kind === 'overlap');
  assert.equal(o.length, 1);
  assert.deepEqual([o[0].via, o[0].otherVia].sort(), ['cabin', 'home-router']);
});

const peers = [
  { name: 'home', address: '10.66.0.10', lan: ['192.168.1.0/24'], allowed_ips: ['10.66.0.0/24'], connected: true },
  { name: 'phone', address: '10.66.0.2', lan: [], allowed_ips: [], connected: false },
  { name: 'laptop', address: '10.66.0.3', lan: [], allowed_ips: ['0.0.0.0/0', '::/0'], connected: true },
  { name: 'old', address: '10.66.0.4', lan: ['10.99.0.0/16'], allowed_ips: [], disabled: true },
];
const defaults = { allowed_ips: ['10.66.0.0/24', '192.168.1.0/24', '172.28.0.0/24'] };

test('WireGuard: LANs behind peers, networks through the server, the internet', () => {
  const m = buildWGMap({ subnet: '10.66.0.0/24', serverIp: '10.66.0.1', peers, defaults });
  const n = byKey(m);
  // the phone (defaults) and the laptop (everything) reach the home LAN
  assert.equal(n['lan:192.168.1.0/24'].via, 'home');
  assert.equal(n['lan:192.168.1.0/24'].reachedBy, 'all');
  // the edge network is through the server; home's own allowed IPs do not include it
  assert.deepEqual([...n['srv:172.28.0.0/24'].reachedBy].sort(), ['laptop', 'phone']);
  // disabled peers and the VPN subnet are left out
  assert.ok(!n['lan:10.99.0.0/16'] && !n['srv:10.66.0.0/24']);
  assert.deepEqual(m.clients.map((c) => c.name), ['home', 'laptop', 'phone']);
  assert.deepEqual(m.internet, ['laptop']);
  assert.deepEqual(m.problems, []);
});

test('WireGuard: a LAN nobody sends to is a problem', () => {
  const m = buildWGMap({ subnet: '10.66.0.0/24', peers, defaults: { allowed_ips: ['10.66.0.0/24'] } });
  const n = byKey(m)['lan:192.168.1.0/24'];
  assert.deepEqual(n.reachedBy, ['laptop']); // only the full-tunnel laptop
  const m2 = buildWGMap({ subnet: '10.66.0.0/24', peers: peers.filter((p) => p.name !== 'laptop'), defaults: { allowed_ips: ['10.66.0.0/24'] } });
  assert.deepEqual(m2.problems.map((p) => p.kind), ['unreached']);
});

test('checkNetwork: what the routing editors accept', () => {
  const opts = { list: ['10.20.0.0/16'], subnet: '10.8.0.0/24' };
  assert.deepEqual(checkNetwork('192.168.1.0/24', opts), { value: '192.168.1.0/24', note: '' });
  assert.deepEqual(checkNetwork('192.168.1.7/24', opts), { value: '192.168.1.0/24', note: 'Added as 192.168.1.0/24' });
  assert.deepEqual(checkNetwork(' 10.1.2.3 ', opts), { value: '10.1.2.3/32', note: '' }); // a host
  assert.match(checkNetwork('192.168.1.0/33', opts).error, /not an IPv4 network/);
  assert.match(checkNetwork('home lan', opts).error, /not an IPv4 network/);
  assert.match(checkNetwork('0.0.0.0/0', opts).error, /all traffic/);
  assert.match(checkNetwork('10.8.0.0/16', opts).error, /overlaps the VPN network/);
  assert.match(checkNetwork('10.8.0.5', opts).error, /overlaps the VPN network/);
  assert.match(checkNetwork('10.20.0.0/16', opts).error, /already there/);
  assert.deepEqual(checkNetwork('', opts), { error: '' });
  // WireGuard allowed IPs may contain the VPN network
  assert.deepEqual(checkNetwork('10.8.0.0/24', { ...opts, allowSubnet: true }), { value: '10.8.0.0/24', note: '' });
});

// A server set up by openvpn-ui: client-to-client, LANs behind routers (iroute),
// routes pushed per client, no "route" lines, the networks named afterwards.
test('an openvpn-ui style server: no route warnings, routers, names, who reaches what', () => {
  const s = { subnet: '10.8.0.0/24', server_ip: '10.8.0.1', pushes_full_tunnel: false, routes: [] };
  const cs = [
    { name: 'docker', static_ip: '', full_tunnel: false, routes: [], lan: [], named: [] },
    { name: 'castle', static_ip: '10.8.0.222', full_tunnel: false, routes: [], lan: [], named: [] },
    { name: 'cabin', static_ip: '10.8.0.39', full_tunnel: false, routes: [], lan: [], named: [] },
    { name: 'phone', static_ip: '10.8.0.60', full_tunnel: true, routes: [], lan: [], named: [] },
    { name: 'laptop', static_ip: '', full_tunnel: false, routes: [], lan: [], named: [] },
  ];
  const nets = [
    { name: 'Docker services', networks: ['172.20.0.0/24'], via: 'docker', clients: ['castle', 'cabin', 'laptop'] },
    { name: 'Castle LAN', networks: ['192.168.150.0/24'], via: 'castle', clients: ['cabin'] },
    { name: 'Cabin LAN', networks: ['192.168.160.0/24'], via: 'cabin', clients: ['castle'] },
    { name: 'Spare LAN', networks: ['192.168.170.0/24'], via: 'laptop', clients: [] },
  ];
  const m = buildMap({ srv: s, clients: cs, nets, online: new Set(['castle']), c2c: true });
  const n = byKey(m);
  assert.deepEqual(m.problems.map((p) => p.kind), ['unreached']); // only Spare LAN
  assert.equal(m.problems[0].name, 'Spare LAN');
  assert.ok(m.networks.every((x) => x.ok && x.missingRoute.length === 0));
  assert.deepEqual(n['named:Docker services'].serverOnly, ['172.20.0.0/24']);
  assert.deepEqual(n['named:Castle LAN'].reachedBy, ['cabin']);
  // routers first (castle is online), then the rest by name
  assert.deepEqual(m.clients.map((c) => c.name), ['castle', 'cabin', 'docker', 'laptop', 'phone']);
  assert.deepEqual(m.internet, ['phone']);
  // the same server without client-to-client needs a route for every LAN
  const m2 = buildMap({ srv: s, clients: cs, nets, c2c: false });
  assert.equal(m2.problems.filter((p) => p.kind === 'route').length, 4);
});

test('fold', () => {
  const l = [1, 2, 3, 4, 5, 6, 7, 8];
  assert.deepEqual(fold(l, false), { shown: [1, 2, 3, 4], more: 4 });
  assert.deepEqual(fold(l, true), { shown: l, more: 0 });
  assert.deepEqual(fold([1, 2, 3, 4, 5], false), { shown: [1, 2, 3, 4, 5], more: 0 }); // no "+1 more"
  assert.deepEqual(fold([], false), { shown: [], more: 0 });
});
