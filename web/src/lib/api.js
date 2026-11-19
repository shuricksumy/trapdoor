import { navigate, servers, session, setServers } from './state.svelte.js';

export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

// Paths that do not belong to one VPN server.
const hostPaths = /^\/(auth\/|status$|servers($|\/)|(hosts|nodes)($|\/)|backup$|backups($|\/)|email($|\/))/;

/**
 * URL of an API path. Per-server paths (everything but auth and status) go
 * to the selected server: '/certs' → '/api/v1/servers/openvpn/certs'.
 */
export function apiURL(path) {
  if (hostPaths.test(path)) return '/api/v1' + path;
  return `/api/v1/servers/${encodeURIComponent(servers.current || 'openvpn')}${path}`;
}

/** Call an API path (see apiURL). JSON in and out; 401 sends the user to the login page. */
export async function api(path, { method = 'GET', body, signal } = {}) {
  const res = await fetch(apiURL(path), {
    method,
    signal,
    credentials: 'same-origin',
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401 && path !== '/auth/login') {
    session.user = null;
    if (location.pathname !== '/login') {
      navigate('/login?next=' + encodeURIComponent(location.pathname + location.search), { replace: true });
    }
    throw new ApiError(401, 'Your session ended. Log in again.');
  }
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = (await res.json()).error || msg;
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, msg);
  }
  if (res.status === 204) return null;
  return (res.headers.get('content-type') || '').includes('json') ? res.json() : res.text();
}

/** POST body to path and save the answer as a file (its name from Content-Disposition). */
export async function download(path, body, fallbackName) {
  const res = await fetch(path.startsWith('/api/') ? path : apiURL(path), {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = (await res.json()).error || msg;
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, msg);
  }
  const blob = await res.blob();
  const name = /filename="([^"]+)"/.exec(res.headers.get('content-disposition') || '')?.[1] ?? fallbackName;
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 10_000);
  return { name, size: blob.size };
}

/** POST a form with a file (multipart); JSON back. */
export async function upload(path, form) {
  const res = await fetch(path.startsWith('/api/') ? path : apiURL(path), { method: 'POST', credentials: 'same-origin', body: form });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, data.error || res.statusText);
  return data;
}

/** Poll fn every ms while the page is visible; returns a stop function. */
export function poll(fn, ms) {
  let timer;
  const tick = async () => {
    if (document.visibilityState === 'visible') {
      try {
        await fn();
      } catch {
        /* the caller shows its own error state */
      }
    }
    timer = setTimeout(tick, ms);
  };
  tick();
  return () => clearTimeout(timer);
}

/** Fetch the server list again (after adding or removing a server). */
export async function reloadServers() {
  const st = await api('/status');
  setServers(st.agent?.servers ?? [], st.agent?.hosts ?? [], st.agent?.containers ?? null, st.agent ?? {});
  return st;
}
