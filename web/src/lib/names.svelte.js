import { api } from './api.js';
import { servers } from './state.svelte.js';

// Display names of OpenVPN clients (set on the Clients page and kept with the
// certificate). Every listing shows the display name and falls back to the
// login (the certificate name) when there is none.
const names = $state({ server: '', map: {} });

/** Take the display names from a /certs answer the page already has. */
export function setClientNames(certs) {
  const map = {};
  for (const c of certs ?? []) if (c.notes && c.kind !== 'server') map[c.name] = c.notes;
  names.server = servers.current;
  names.map = map;
}

/** Load the display names of the selected server's clients (no-op for WireGuard). */
export async function loadClientNames() {
  const type = servers.list?.find((s) => s.id === servers.current)?.type;
  if (type && type !== 'openvpn') return setClientNames([]);
  try {
    setClientNames(await api('/certs'));
  } catch {
    /* names are optional: logins are shown instead */
  }
}

/** The display name of a client, or its login. */
export function clientName(login) {
  if (!login || names.server !== servers.current) return login;
  return names.map[login] || login;
}

/** Tooltip with the login when a display name is shown instead. */
export function clientTitle(login) {
  const n = clientName(login);
  return n !== login ? `Login: ${login}` : undefined;
}
