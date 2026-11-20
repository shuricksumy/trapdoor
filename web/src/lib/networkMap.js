// The OpenVPN network map as plain data, so it can be tested: which networks
// exist, where they are (through the server or behind a client), who reaches
// them, and what is only half set up.
//
// input: {
//   srv:     GET /network (subnet, server_ip, pushes_full_tunnel, routes, client_lans)
//   clients: GET /network/clients ([{ name, static_ip, full_tunnel, routes, lan, named }])
//   nets:    named networks ([{ name, description, networks, via, clients }])
//   pushed:  routes server.conf pushes to every client
//   online:  Set of client names connected now
//   c2c:     server.conf has client-to-client: OpenVPN carries traffic from clients to
//            LANs behind other clients itself, so a server "route" is only needed for
//            traffic from the server; a missing one is then not a problem
// }

/** IPv4 CIDR (or bare address = /32) → [network as uint32, bits], or null. */
export function parse(cidr) {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})(?:\/(\d{1,2}))?$/.exec(String(cidr ?? '').trim());
  if (!m) return null;
  const o = m.slice(1, 5).map(Number);
  const bits = m[5] === undefined ? 32 : Number(m[5]);
  if (o.some((x) => x > 255) || bits > 32) return null;
  const ip = ((o[0] << 24) | (o[1] << 16) | (o[2] << 8) | o[3]) >>> 0;
  const mask = bits === 0 ? 0 : (~0 << (32 - bits)) >>> 0;
  return [(ip & mask) >>> 0, bits];
}

/** "192.168.1.5/24" → "192.168.1.0/24" (a bare address stays /32), or null if it is not IPv4. */
export function normalize(cidr) {
  const p = parse(cidr);
  if (!p) return null;
  const ip = [24, 16, 8, 0].map((s) => (p[0] >>> s) & 255).join('.');
  return `${ip}/${p[1]}`;
}

/** Whether networks a and b share any address. */
export const overlaps = (a, b) => covers(a, b) || covers(b, a);

/** Whether network a contains network b. */
export function covers(a, b) {
  const pa = parse(a);
  const pb = parse(b);
  if (!pa || !pb || pa[1] > pb[1]) return false;
  const mask = pa[1] === 0 ? 0 : (~0 << (32 - pa[1])) >>> 0;
  return ((pb[0] & mask) >>> 0) === pa[0];
}

const anyCovers = (list, cidr) => (list ?? []).some((r) => covers(r, cidr));
const uniq = (xs) => [...new Set(xs)];

/** The map: clients, networks, the internet and the problems, in display order. */
export function buildMap({ srv, clients = [], nets = [], pushed = [], online = new Set(), c2c = false }) {
  const routes = srv?.routes ?? [];
  const named = nets.map((n) => ({ ...n, networks: n.networks ?? [], clients: n.clients ?? [] }));
  const inNamed = (cidr) => named.some((n) => n.networks.includes(cidr));

  // What each client reaches: its own routes, the named networks it uses, what every client gets.
  const reach = new Map(clients.map((c) => [c.name, [...(c.routes ?? []), ...named.filter((n) => n.clients.includes(c.name)).flatMap((n) => n.networks), ...pushed]]));
  const reachedBy = (cidrs, via) => {
    if (cidrs.length && cidrs.every((x) => anyCovers(pushed, x))) return 'all';
    return clients.filter((c) => c.name !== via && cidrs.some((x) => anyCovers(reach.get(c.name), x))).map((c) => c.name);
  };

  const networks = [];
  // named networks
  for (const n of named) {
    networks.push({ key: `named:${n.name}`, name: n.name, named: true, description: n.description ?? '', cidrs: n.networks, via: n.via || '' });
  }
  // LANs behind clients that are not part of a named network
  for (const c of clients) {
    for (const cidr of c.lan ?? []) if (!inNamed(cidr)) networks.push({ key: `lan:${cidr}`, name: cidr, named: false, cidrs: [cidr], via: c.name });
  }
  // networks reached through the server: routes given to clients that nothing has behind it
  const behind = networks.filter((n) => n.via).flatMap((n) => n.cidrs);
  const through = uniq([...pushed, ...clients.flatMap((c) => c.routes ?? [])]).filter((cidr) => !inNamed(cidr) && !behind.some((b) => covers(cidr, b) || covers(b, cidr)));
  for (const cidr of through) networks.push({ key: `srv:${cidr}`, name: cidr, named: false, cidrs: [cidr], via: '' });

  for (const n of networks) {
    n.reachedBy = reachedBy(n.cidrs, n.via);
    // a LAN behind a client needs a server.conf "route" so the server sends it into the VPN
    const missing = n.via ? n.cidrs.filter((x) => !anyCovers(routes, x)) : [];
    n.missingRoute = c2c ? [] : missing;
    // with client-to-client: clients reach it, the server itself does not
    n.serverOnly = c2c ? missing : [];
    n.ok = n.missingRoute.length === 0;
  }

  const routers = new Map();
  for (const n of networks) if (n.via) routers.set(n.via, [...(routers.get(n.via) ?? []), n.name]);
  const clientNodes = clients
    .map((c) => ({ name: c.name, ip: c.static_ip || '', online: online.has(c.name), full: !!c.full_tunnel, router: routers.get(c.name) ?? [] }))
    .sort((a, b) => b.router.length - a.router.length || Number(b.online) - Number(a.online) || a.name.localeCompare(b.name));

  const internet = srv?.pushes_full_tunnel ? 'all' : clients.filter((c) => c.full_tunnel).map((c) => c.name);

  const problems = [];
  for (const n of networks) {
    for (const cidr of n.missingRoute) problems.push({ kind: 'route', cidr, network: n.key, name: n.name, via: n.via });
    if (n.via && n.reachedBy !== 'all' && n.reachedBy.length === 0) problems.push({ kind: 'unreached', network: n.key, name: n.name, via: n.via });
  }
  problems.push(...overlapProblems(networks));
  // server.conf routes into the VPN that no client has behind it: traffic for them goes nowhere
  const lans = networks.filter((n) => n.via).flatMap((n) => n.cidrs);
  for (const r of routes) if (!lans.some((l) => covers(r, l) || covers(l, r))) problems.push({ kind: 'orphan', cidr: r });

  return { clients: clientNodes, networks, internet, problems };
}

/** LANs behind two different clients that share addresses: traffic for them can only go one way. */
export function overlapProblems(networks) {
  const out = [];
  const lans = networks.filter((n) => n.via).flatMap((n) => n.cidrs.map((cidr) => ({ cidr, n })));
  for (let i = 0; i < lans.length; i++) {
    for (let j = i + 1; j < lans.length; j++) {
      const a = lans[i];
      const b = lans[j];
      if (a.n.via !== b.n.via && overlaps(a.cidr, b.cidr)) {
        out.push({ kind: 'overlap', cidr: a.cidr, other: b.cidr, name: a.n.name, via: a.n.via, otherName: b.n.name, otherVia: b.n.via, network: a.n.key });
      }
    }
  }
  return out;
}

const ALL4 = '0.0.0.0/0';
const isAll = (c) => c === ALL4 || c === '::/0';

/**
 * The WireGuard / AmneziaWG map in the same shape as buildMap.
 * input: { subnet, serverIp, peers: GET /peers, defaults: GET /settings }
 * A client sends what its allowed IPs say (its own, or the server's defaults).
 */
export function buildWGMap({ subnet = '', serverIp = '', peers = [], defaults = null }) {
  const active = peers.filter((p) => !p.disabled && !p.expired);
  const sends = (p) => (p.allowed_ips?.length ? p.allowed_ips : defaults?.allowed_ips ?? []);
  const reachedBy = (cidr, via) => {
    const who = active.filter((p) => p.name !== via && sends(p).some((r) => isAll(r) || covers(r, cidr))).map((p) => p.name);
    const others = active.filter((p) => p.name !== via);
    return others.length > 0 && who.length === others.length ? 'all' : who;
  };

  const networks = [];
  for (const p of active) for (const cidr of p.lan ?? []) networks.push({ key: `lan:${cidr}`, name: cidr, named: false, cidrs: [cidr], via: p.name });
  const behind = networks.map((n) => n.cidrs[0]);
  const through = [...new Set(active.flatMap(sends))].filter(
    (c) => !isAll(c) && !c.includes(':') && !(subnet && overlaps(c, subnet)) && !behind.some((b) => overlaps(b, c)),
  );
  for (const cidr of through) networks.push({ key: `srv:${cidr}`, name: cidr, named: false, cidrs: [cidr], via: '' });
  for (const n of networks) {
    n.reachedBy = reachedBy(n.cidrs[0], n.via);
    n.missingRoute = [];
    n.serverOnly = [];
    n.ok = true;
  }

  const routers = new Map();
  for (const n of networks) if (n.via) routers.set(n.via, [...(routers.get(n.via) ?? []), n.name]);
  const clients = active
    .map((p) => ({ name: p.name, ip: p.address ?? '', online: !!p.connected, full: sends(p).some(isAll), router: routers.get(p.name) ?? [] }))
    .sort((a, b) => b.router.length - a.router.length || Number(b.online) - Number(a.online) || a.name.localeCompare(b.name));
  const full = clients.filter((c) => c.full).map((c) => c.name);
  const internet = clients.length > 0 && full.length === clients.length ? 'all' : full;

  const problems = [];
  for (const n of networks) if (n.via && n.reachedBy !== 'all' && n.reachedBy.length === 0) problems.push({ kind: 'unreached', network: n.key, name: n.name, via: n.via, cidr: n.cidrs[0] });
  problems.push(...overlapProblems(networks));
  return { clients, networks, internet, problems, server: { ip: serverIp, subnet } };
}
