// node --test: the Access diagram's switches.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { destinations, toggle, stateOf } from './accessRules.js';

const ctx = { serverIp: '10.8.0.1', subnet: '10.8.0.0/24', routed: ['192.168.1.0/24'], bypass: false };
const ips = { anna: '10.8.0.50', bob: '10.8.0.51' };
// a typical address in each destination
const probe = { server: '10.8.0.1', clients: '10.8.0.51', routed: '192.168.1.20', internet: '1.1.1.1' };

const num = (ip) => ip.split('.').reduce((n, o) => n * 256 + Number(o), 0);
function inNet(ip, cidr) {
  const [net, bits = '32'] = cidr.split('/');
  const mask = bits === '0' ? 0 : (~0 << (32 - Number(bits))) >>> 0;
  return ((num(ip) & mask) >>> 0) === ((num(net) & mask) >>> 0);
}
// First match decides, like the TD- chain; any-protocol traffic from client to dst.
function verdict(policy, client, dst) {
  if (!policy.enabled) return true;
  for (const r of policy.rules) {
    if (r.client && r.client !== client) continue;
    if (r.to && !inNet(dst, r.to)) continue;
    if (r.proto && r.proto !== 'any') continue; // a port/protocol rule does not decide for all traffic
    return r.action === 'allow';
  }
  return policy.default === 'allow';
}
// The diagram must show what a packet from a client without own rules gets.
function agrees(policy, c = ctx) {
  for (const d of destinations(policy, c)) {
    if (!d.usable) continue;
    assert.equal(d.ok, verdict(policy, 'bob', probe[d.key]), `${d.key}: diagram says ${d.ok ? 'allowed' : 'blocked'}; rules: ${JSON.stringify(policy.rules)} default ${policy.default}`);
  }
}
const click = (p, key, c = ctx) => ({ ...p, ...toggle(p, c, key) });
const states = (p, c = ctx) => Object.fromEntries(destinations(p, c).map((d) => [d.key, d.ok]));

test('a full click sequence writes the expected rules and matches real first-match results', () => {
  let p = { enabled: true, default: 'deny', rules: [] };
  assert.deepEqual(states(p), { server: false, clients: false, routed: false, internet: false });
  agrees(p);

  p = click(p, 'internet'); // allow the internet, keep the rest blocked
  assert.equal(p.default, 'allow');
  assert.deepEqual(p.rules, [
    { action: 'deny', to: '10.8.0.1' },
    { action: 'deny', to: '10.8.0.0/24' },
    { action: 'deny', to: '192.168.1.0/24' },
  ]);
  assert.deepEqual(states(p), { server: false, clients: false, routed: false, internet: true });
  agrees(p);

  p = click(p, 'server'); // the server's rule must stay ahead of the VPN network's
  assert.deepEqual(p.rules.map((r) => `${r.action} ${r.to}`), ['allow 10.8.0.1', 'deny 10.8.0.0/24', 'deny 192.168.1.0/24']);
  agrees(p);

  p = click(p, 'clients'); // equal to the default now: no rules left for server and clients
  assert.deepEqual(p.rules, [{ action: 'deny', to: '192.168.1.0/24' }]);
  agrees(p);

  p = click(p, 'internet'); // block the internet, keep server and clients allowed
  assert.equal(p.default, 'deny');
  assert.deepEqual(p.rules.map((r) => `${r.action} ${r.to}`), ['allow 10.8.0.1', 'allow 10.8.0.0/24']);
  assert.deepEqual(states(p), { server: true, clients: true, routed: false, internet: false });
  agrees(p);
});

test('every reachable state from every start agrees with first-match evaluation', () => {
  const keys = ['server', 'clients', 'routed', 'internet'];
  for (const def of ['allow', 'deny']) {
    // all 4^4 click sequences of length 4
    for (let n = 0; n < 256; n++) {
      let p = { enabled: true, default: def, rules: [{ client: 'anna', to: '192.168.1.0/24', action: 'allow' }] };
      for (let i = 0, m = n; i < 4; i++, m >>= 2) {
        const before = states(p);
        const k = keys[m & 3];
        p = click(p, k);
        const after = states(p);
        for (const o of keys) assert.equal(after[o], o === k ? !before[o] : before[o], `click ${k} changed ${o}`);
        agrees(p);
        // Anna's own rule stays first and keeps her LAN access
        assert.deepEqual(p.rules[0], { client: 'anna', to: '192.168.1.0/24', action: 'allow' });
        assert.equal(verdict(p, 'anna', probe.routed), true);
      }
    }
  }
});

test('single-client and port rules stay first; quick rules go before a rule for anywhere', () => {
  const p0 = {
    enabled: true,
    default: 'allow',
    rules: [
      { client: 'anna', to: '10.8.0.1', proto: 'tcp', ports: '443', action: 'allow' },
      { client: 'bob', action: 'deny' },
    ],
  };
  const p = click(p0, 'routed');
  assert.deepEqual(p.rules, [p0.rules[0], { action: 'deny', to: '192.168.1.0/24' }, p0.rules[1]]);
  // the rules besides the switch are counted
  const d = Object.fromEntries(destinations(p, ctx).map((x) => [x.key, x]));
  assert.equal(d.server.more, 2); // anna's port rule and bob's "anywhere"
  assert.equal(d.routed.more, 1);
  assert.equal(d.internet.more, 1);
});

test('rules saved by the agent as /32 are recognised, and their notes are kept', () => {
  const p0 = { enabled: true, default: 'deny', rules: [{ action: 'allow', to: '10.8.0.1/32', note: 'DNS and web UI' }] };
  assert.equal(stateOf(p0, ctx, ['10.8.0.1']), true);
  const p = click(p0, 'routed');
  assert.deepEqual(p.rules, [
    { action: 'allow', to: '10.8.0.1', note: 'DNS and web UI' },
    { action: 'allow', to: '192.168.1.0/24' },
  ]);
  agrees(p);
});

test('a rule for the VPN network covers the server unless it has its own', () => {
  const p = { enabled: true, default: 'allow', rules: [{ action: 'deny', to: '10.8.0.0/24' }] };
  assert.equal(states(p).server, false);
  agrees(p);
});

test('client-to-client: other clients cannot be switched and get no rule', () => {
  const c = { ...ctx, bypass: true };
  let p = { enabled: true, default: 'deny', rules: [] };
  const d = destinations(p, c).find((x) => x.key === 'clients');
  assert.equal(d.usable, false);
  assert.equal(d.bypass, true);
  assert.deepEqual(toggle(p, c, 'clients'), { default: 'deny', rules: [] });
  p = click(p, 'internet', c);
  assert.deepEqual(p.rules.map((r) => `${r.action} ${r.to}`), ['deny 10.8.0.1', 'deny 192.168.1.0/24']);
  agrees(p, c);
});

test('nothing changes while the rules are off or a destination has no network', () => {
  const off = { enabled: false, default: 'deny', rules: [] };
  assert.deepEqual(toggle(off, ctx, 'server'), { default: 'deny', rules: [] });
  const c = { ...ctx, routed: [] };
  const p = { enabled: true, default: 'deny', rules: [] };
  assert.equal(destinations(p, c).find((x) => x.key === 'routed').usable, false);
  assert.deepEqual(toggle(p, c, 'routed'), { default: 'deny', rules: [] });
  // with no routed networks, flipping the internet writes no empty rules
  assert.deepEqual(click(p, 'internet', c).rules.map((r) => r.to), ['10.8.0.1', '10.8.0.0/24']);
});
