// App-wide state: router, signed-in user, theme, toasts.

export const route = $state({ path: location.pathname, query: new URLSearchParams(location.search) });

export function navigate(to, { replace = false } = {}) {
  const url = new URL(to, location.origin);
  if (url.pathname + url.search === location.pathname + location.search) return;
  history[replace ? 'replaceState' : 'pushState']({}, '', url.pathname + url.search);
  route.path = url.pathname;
  route.query = url.searchParams;
  window.scrollTo(0, 0);
}

/** Update query parameters of the current page without adding history entries. */
export function setQuery(params) {
  const q = new URLSearchParams(location.search);
  for (const [k, v] of Object.entries(params)) {
    if (v === '' || v == null) q.delete(k);
    else q.set(k, v);
  }
  const s = q.toString();
  history.replaceState({}, '', location.pathname + (s ? '?' + s : ''));
  route.query = q;
}

addEventListener('popstate', () => {
  route.path = location.pathname;
  route.query = new URLSearchParams(location.search);
});

// Same-origin <a href> clicks become client-side navigation.
addEventListener('click', (e) => {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  const a = e.target.closest?.('a[href]');
  if (!a || a.target || a.hasAttribute('download')) return;
  const url = new URL(a.href);
  if (url.origin !== location.origin || url.pathname.startsWith('/api/')) return;
  e.preventDefault();
  navigate(url.pathname + url.search);
});

export const session = $state({ user: null, checked: false });

// VPN servers (from /api/v1/status) and the one the pages show.
// hosts: the other machines with an agent (their servers have a "host").
// containers: where this machine runs each type (type → container name).
export const servers = $state({ list: [], current: '', hosts: [], containers: null });

export function selectServer(id) {
  servers.current = id;
  try {
    localStorage.setItem('td-server', id);
  } catch {
    /* private mode: keep for this visit only */
  }
}

/** The selected server (id, type, name, …), or undefined before the list is loaded. */
export function currentServer() {
  return servers.list.find((s) => s.id === servers.current);
}

/** Keep the selected server if it still exists, else the remembered one, else the first one. */
export function setServers(list, hosts = [], containers = null) {
  servers.list = list;
  servers.hosts = hosts;
  servers.containers = containers;
  if (list.some((s) => s.id === servers.current)) return;
  let saved = '';
  try {
    saved = localStorage.getItem('td-server') || '';
  } catch {
    /* ignore */
  }
  servers.current = list.some((s) => s.id === saved) ? saved : (list[0]?.id ?? '');
}

// Server-side settings shown in the UI (from /api/v1/status).
export const settings = $state({ expiryWarnDays: 30, setupNeeded: false });

// Theme: 'light' | 'dark' | 'auto'
const media = matchMedia('(prefers-color-scheme: dark)');
function load() {
  try {
    return localStorage.getItem('td-theme') || 'auto';
  } catch {
    return 'auto';
  }
}
export const theme = $state({ choice: load() });

function apply() {
  const t = theme.choice === 'auto' ? (media.matches ? 'dark' : 'light') : theme.choice;
  document.documentElement.dataset.theme = t;
}
export function setTheme(choice) {
  theme.choice = choice;
  try {
    localStorage.setItem('td-theme', choice);
  } catch {
    /* private mode: keep for this visit only */
  }
  apply();
}
media.addEventListener('change', () => theme.choice === 'auto' && apply());

export const toasts = $state([]);
let toastId = 0;
export function toast(text, kind = 'ok') {
  const id = ++toastId;
  toasts.push({ id, text, kind });
  setTimeout(() => {
    const i = toasts.findIndex((t) => t.id === id);
    if (i >= 0) toasts.splice(i, 1);
  }, 4000);
}
