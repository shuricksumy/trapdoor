import { navigate, session } from './state.svelte.js';

export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

/** Call /api/v1<path>. JSON in and out; 401 sends the user to the login page. */
export async function api(path, { method = 'GET', body, signal } = {}) {
  const res = await fetch('/api/v1' + path, {
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
