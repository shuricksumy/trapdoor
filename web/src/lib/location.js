// Where a server runs: machine › node › container.
//
// A node is an agent with its VPN container(s): this web UI's own node ('' in
// ids and API calls) and the nodes that joined or were added under Nodes
// (their servers have a "host"). Several nodes can share a machine
// (TD_MACHINE); a node without one is a machine of its own.

import { servers } from './state.svelte.js';

export const typeNames = { openvpn: 'OpenVPN', wireguard: 'WireGuard', amneziawg: 'AmneziaWG' };

const hostKey = (hostOrServer) => (typeof hostOrServer === 'string' ? hostOrServer : (hostOrServer?.host ?? ''));

/**
 * A node by its name ('' = the own one): { key, name, machine, containers, error, hostname, version, url }.
 */
export function nodeOf(hostOrServer) {
  const key = hostKey(hostOrServer);
  if (!key) {
    return { key: '', name: servers.node || 'main', machine: servers.machine || 'This machine', containers: servers.containers, own: true };
  }
  const h = servers.hosts.find((x) => x.name === key);
  return { key, name: key, machine: h?.machine || key, containers: h?.containers ?? null, error: h?.error, hostname: h?.hostname, version: h?.version, url: h?.url };
}

/** All nodes grouped by machine: [{ machine, nodes: [...] }], the own machine first. */
export function machines() {
  const all = [nodeOf(''), ...servers.hosts.map((h) => nodeOf(h.name))];
  const out = [];
  for (const n of all) {
    let g = out.find((m) => m.machine === n.machine);
    if (!g) out.push((g = { machine: n.machine, nodes: [] }));
    g.nodes.push(n);
  }
  return out;
}

/** "node main on this machine", "node vps on machine vps-frankfurt" for a node or a server. */
export function nodeTitle(hostOrServer) {
  const n = nodeOf(hostOrServer);
  return n.own && !servers.machine ? `node ${n.name} on this machine` : `node ${n.name} on machine ${n.machine}`;
}

/** Kept for older callers: the node title. */
export const machineName = nodeTitle;

/** "This machine › node main · container vpn-wireguard" for a server. */
export function where(s) {
  if (!s) return '';
  const n = nodeOf(s);
  const c = s.container || '';
  return `${n.machine} › node ${n.name}${c ? ` · container ${c}` : ''}`;
}

/** The container a new server of a type would run in on a node ('' = the own one). */
export function containerFor(type, host = '') {
  return nodeOf(host).containers?.[type] ?? '';
}

/** Whether a node can run a type at all (when it says what it runs). */
export function canRun(type, host = '') {
  const map = nodeOf(host).containers;
  return !map || type in map;
}
