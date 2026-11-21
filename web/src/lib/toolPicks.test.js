import { test } from 'node:test';
import assert from 'node:assert/strict';
import { gateway, toolPicks } from './toolPicks.js';

test('gateway', () => {
  assert.equal(gateway('192.168.1.0/24'), '192.168.1.1');
  assert.equal(gateway('10.50.0.0/20'), '10.50.0.1');
  assert.equal(gateway('10.0.0.5/32'), '10.0.0.5');
  assert.equal(gateway('fd00::/64'), null);
  assert.equal(gateway(''), null);
});

test('toolPicks groups, names LANs and drops duplicates', () => {
  const g = toolPicks({
    serverIP: '10.8.0.1',
    sessions: [{ name: 'zed', virtual_address: '10.8.0.9' }, { name: 'alice', virtual_address: '10.8.0.6' }, { name: 'bob' }],
    lans: [['192.168.50.0/24', 'alice'], ['192.168.60.0/24', 'zed']],
    nets: [{ name: 'Office LAN', networks: ['192.168.50.0/24'] }],
    dns: ['10.8.0.1', '9.9.9.9'],
    display: (n) => (n === 'alice' ? 'Alice' : n),
  });
  assert.deepEqual(g.map((x) => x.key), ['server', 'online', 'lans', 'dns', 'net']);
  assert.deepEqual(g[1].items.map((i) => i.name), ['Alice', 'zed']); // sorted, no address → left out
  assert.deepEqual(g[2].items, [
    { v: '192.168.50.1', name: 'Office LAN', title: 'Gateway of 192.168.50.0/24, behind Alice' },
    { v: '192.168.60.1', name: '192.168.60.0/24', title: 'Gateway of 192.168.60.0/24, behind zed' },
  ]);
  assert.deepEqual(g[3].items.map((i) => i.v), ['9.9.9.9']); // 10.8.0.1 is already the server
});

test('toolPicks with nothing known: only the internet', () => {
  assert.deepEqual(toolPicks().map((x) => x.key), ['net']);
});
