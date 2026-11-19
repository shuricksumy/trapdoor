// Where a server runs: the machine (this one, or a host added under Hosts)
// and the container (TD_CONTAINER_* of that machine's agent).

import { servers } from './state.svelte.js';

export const typeNames = { openvpn: 'OpenVPN', wireguard: 'WireGuard', amneziawg: 'AmneziaWG' };

/** The machine's name for a server or a host name ('' = this machine). */
export function machineName(hostOrServer) {
  const h = typeof hostOrServer === 'string' ? hostOrServer : (hostOrServer?.host ?? '');
  return h || 'This machine';
}

/** "This machine · vpn-wireguard" for a server (the container only when known). */
export function where(s) {
  if (!s) return '';
  return s.container ? `${machineName(s)} · container ${s.container}` : machineName(s);
}

/** The container a new server of a type would run in on a machine ('' = this one). */
export function containerFor(type, host = '') {
  const map = host ? servers.hosts.find((h) => h.name === host)?.containers : servers.containers;
  return map?.[type] ?? '';
}

/** Whether a machine can run a type at all (when it says what it runs). */
export function canRun(type, host = '') {
  const map = host ? servers.hosts.find((h) => h.name === host)?.containers : servers.containers;
  return !map || type in map;
}
