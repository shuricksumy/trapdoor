// Suggested targets for the Tools page, grouped: this server, connected
// clients, the gateways of LANs behind clients (by their named network when
// there is one), DNS servers for clients and the internet.

/** The first address of a CIDR (its usual gateway): 192.168.1.0/24 → 192.168.1.1. */
export function gateway(cidr) {
  const m = /^(\d+\.\d+\.\d+\.)(\d+)\/(\d+)$/.exec(cidr ?? '');
  if (!m) return null;
  return Number(m[3]) >= 31 ? m[1] + m[2] : m[1] + (Number(m[2]) + 1);
}

/**
 * @param {object} o
 * @param {string} [o.serverIP]   the server's VPN address
 * @param {Array<{name:string, virtual_address?:string}>} [o.sessions]
 * @param {Array<[string, string]>} [o.lans]  [cidr, client]
 * @param {Array<{name:string, networks:string[]}>} [o.nets]  named networks
 * @param {string[]} [o.dns]
 * @param {(n:string)=>string} [o.display]  a client's display name
 * @returns {Array<{key:string, title:string, items:Array<{v:string, name:string, title:string}>}>}
 */
export function toolPicks({ serverIP = '', sessions = [], lans = [], nets = [], dns = [], display = (n) => n } = {}) {
  const seen = new Set();
  const add = (list, v, name, title) => {
    if (!v || seen.has(v)) return;
    seen.add(v);
    list.push({ v, name, title });
  };
  const server = [], online = [], gw = [], dnsList = [], net = [];
  add(server, serverIP, 'This server', 'The VPN server itself');
  for (const s of [...sessions].sort((a, b) => display(a.name).localeCompare(display(b.name)))) {
    add(online, s.virtual_address, display(s.name), `${display(s.name)}, connected`);
  }
  const named = (cidr) => nets.find((n) => (n.networks ?? []).includes(cidr))?.name;
  for (const [cidr, client] of lans) {
    add(gw, gateway(cidr), named(cidr) ?? cidr, `Gateway of ${cidr}, behind ${display(client)}`);
  }
  for (const d of dns) add(dnsList, d, 'DNS', 'DNS server for clients');
  add(net, '1.1.1.1', 'Cloudflare', 'Internet: Cloudflare');
  return [
    ['server', 'Server', server],
    ['online', 'Connected', online],
    ['lans', 'LANs behind clients', gw],
    ['dns', 'DNS', dnsList],
    ['net', 'Internet', net],
  ].filter(([, , items]) => items.length).map(([key, title, items]) => ({ key, title, items }));
}
