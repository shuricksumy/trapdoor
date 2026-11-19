<script>
  import { route, session, navigate, toasts, toast, settings, servers, setServers, selectServer, currentServer } from './lib/state.svelte.js';
  import { api, reloadServers } from './lib/api.js';
  import ThemeSwitch from './lib/ThemeSwitch.svelte';
  import Login from './pages/Login.svelte';
  import Dashboard from './pages/Dashboard.svelte';
  import Certificates from './pages/Certificates.svelte';
  import Sessions from './pages/Sessions.svelte';
  import Statistics from './pages/Statistics.svelte';
  import Account from './pages/Account.svelte';
  import Server from './pages/Server.svelte';
  import Setup from './pages/Setup.svelte';
  import Access from './pages/Access.svelte';
  import WireGuardOverview from './pages/WireGuardOverview.svelte';
  import WireGuardClients from './pages/WireGuardClients.svelte';
  import NewServer from './pages/NewServer.svelte';
  import Hosts from './pages/Hosts.svelte';
  import Servers from './pages/Servers.svelte';

  const pages = [
    { path: '/', label: 'Overview', icon: 'M3 11l9-7 9 7v9a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z', component: Dashboard },
    { path: '/certificates', label: 'Clients', icon: 'M12 3l7 3v5c0 4.5-3 8.5-7 10-4-1.5-7-5.5-7-10V6z', component: Certificates },
    { path: '/sessions', label: 'Connected', icon: 'M4 12h4l2-5 4 10 2-5h4', component: Sessions },
    { path: '/statistics', label: 'Traffic', icon: 'M5 20V11M10 20V5M15 20v-7M20 20V8', component: Statistics },
    { path: '/access', label: 'Access', icon: 'M7 11V8a5 5 0 0 1 10 0v3M5 11h14v10H5zM12 15v2', component: Access },
    { path: '/server', label: 'Server', icon: 'M4 5h16v5H4zM4 14h16v5H4zM8 7.5h.01M8 16.5h.01', component: Server },
  ];

  const wgPages = [
    { path: '/', label: 'Overview', icon: pages[0].icon, component: WireGuardOverview },
    { path: '/clients', label: 'Clients', icon: pages[1].icon, component: WireGuardClients },
    pages[2],
    pages[3],
    pages[4],
  ];

  // WireGuard and AmneziaWG servers share their pages.
  const isWG = $derived(['wireguard', 'amneziawg'].includes(currentServer()?.type));
  const navPages = $derived(isWG ? wgPages : pages);

  const current = $derived(
    route.path === '/login'
      ? { component: Login }
      : route.path === '/account'
        ? { component: Account }
        : route.path === '/servers/new'
          ? { component: NewServer }
          : route.path === '/hosts'
            ? { component: Hosts }
            : route.path === '/servers'
              ? { component: Servers }
          : route.path === '/setup' && !isWG
            ? { component: Setup }
            : (navPages.find((p) => p.path === route.path) ?? navPages[0]),
  );

  // Start the selected server from the "stopped" banner.
  let starting = $state(false);
  async function startCurrent() {
    starting = true;
    try {
      const r = await api('/servers/' + encodeURIComponent(servers.current) + '/start', { method: 'POST' });
      toast(r.note || `${r.name} started.`);
      await reloadServers();
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      starting = false;
    }
  }

  $effect(() => {
    if (session.checked) return;
    api('/auth/me')
      .then((me) => {
        session.user = me;
        if (route.path === '/login') navigate('/', { replace: true });
      })
      .catch(() => {})
      .finally(() => (session.checked = true));
  });

  // Servers and settings, once per login.
  $effect(() => {
    if (!session.user) return;
    api('/status')
      .then((st) => {
        if (st.expiry_warn_days) settings.expiryWarnDays = st.expiry_warn_days;
        setServers(st.agent?.servers ?? [], st.agent?.hosts ?? []);
      })
      .catch(() => {});
  });

  // Setup state of the selected server.
  $effect(() => {
    if (!session.user || !servers.current) return;
    if (currentServer()?.type !== 'openvpn') {
      settings.setupNeeded = false;
      return;
    }
    const id = servers.current;
    api('/setup')
      .then((st) => {
        if (servers.current !== id) return; // another server was picked meanwhile
        settings.setupNeeded = st.needed;
        if (st.needed && route.path === '/') navigate('/setup', { replace: true });
      })
      .catch(() => {});
  });

  $effect(() => {
    const titles = { '/login': 'Log in', '/account': 'Account', '/setup': 'Setup', '/servers/new': 'Add a server', '/hosts': 'Hosts', '/servers': 'Servers' };
    const name = titles[route.path] ?? navPages.find((p) => p.path === route.path)?.label ?? 'Overview';
    document.title = `${name} · trapdoor`;
  });
</script>

{#if !session.checked}
  <div class="boot" aria-busy="true"></div>
{:else if route.path === '/login' || !session.user}
  <Login />
{:else}
  <div class="shell">
    <nav class="rail" aria-label="Main">
      <a class="brand" href="/" aria-label="trapdoor overview">
        <svg viewBox="0 0 32 32" width="26" height="26" aria-hidden="true">
          <path d="M5 22c0-6 5-11 11-11s11 5 11 11" fill="none" stroke="var(--accent)" stroke-width="3" stroke-linecap="round" />
          <path d="M10.5 22c0-3 2.5-5.5 5.5-5.5s5.5 2.5 5.5 5.5" fill="none" stroke="var(--upload)" stroke-width="3" stroke-linecap="round" />
        </svg>
        <span>trapdoor</span>
      </a>
      <ul>
        {#each navPages as p (p.path)}
          <li>
            <a href={p.path} class:active={route.path === p.path} aria-current={route.path === p.path ? 'page' : undefined}>
              <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true">
                <path d={p.icon} fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
              <span>{p.label}</span>
            </a>
          </li>
        {/each}
      </ul>
      <div class="rail-foot">
        <ThemeSwitch />
        <a class="who" href="/account" class:active={route.path === '/account'}>
          <span class="avatar" aria-hidden="true">{session.user.username.slice(0, 1).toUpperCase()}</span>
          <span>{session.user.username}</span>
        </a>
      </div>
    </nav>

    <main>
      <div class="server-bar">
        {#if servers.list.length > 1}
          <label class="server-switch">
            <span>Server</span>
            <select value={servers.current} onchange={(e) => { selectServer(e.currentTarget.value); navigate('/'); }}>
              {#if servers.hosts.length}
                {#each [{ name: '', label: 'This machine' }, ...servers.hosts.map((h) => ({ name: h.name, label: h.name }))] as g (g.name)}
                  {@const list = servers.list.filter((sv) => (sv.host ?? '') === g.name)}
                  {#if list.length}
                    <optgroup label={g.label}>
                      {#each list as sv (sv.id)}<option value={sv.id}>{sv.name}</option>{/each}
                    </optgroup>
                  {/if}
                {/each}
              {:else}
                {#each servers.list as sv (sv.id)}
                  <option value={sv.id}>{sv.name}</option>
                {/each}
              {/if}
            </select>
          </label>
        {/if}
        <span class="bar-links small">
          {#if route.path !== '/servers'}<a class="add-server" href="/servers">Servers ({servers.list.length})</a>{/if}
          {#if route.path !== '/hosts'}<a class="add-server" href="/hosts">Hosts{servers.hosts.length ? ` (${servers.hosts.length + 1})` : ''}</a>{/if}
          {#if route.path !== '/servers/new'}<a class="add-server" href="/servers/new">Add a server</a>{/if}
        </span>
      </div>
      {#if currentServer()?.stopped && !['/servers', '/servers/new', '/hosts', '/account'].includes(route.path)}
        <div class="banner warn stopped-banner" role="status">
          <span><strong>{currentServer().name}</strong> is stopped: clients cannot connect.</span>
          <button class="btn small primary" disabled={starting} onclick={startCurrent}>{starting ? 'Starting…' : 'Start'}</button>
        </div>
      {/if}
      {#key servers.current}
        <current.component />
      {/key}
    </main>
  </div>
{/if}

<div class="toasts" role="status" aria-live="polite">
  {#each toasts as t (t.id)}
    <div class="toast {t.kind}">{t.text}</div>
  {/each}
</div>

<style>
  .boot { min-height: 100vh; }

  .shell {
    display: grid;
    grid-template-columns: var(--rail) 1fr;
    min-height: 100vh;
    /* rail colour runs the full page height, also on long pages */
    background: linear-gradient(to right, var(--surface) calc(var(--rail) - 1px), var(--line) calc(var(--rail) - 1px) var(--rail), transparent var(--rail));
  }

  .rail {
    position: sticky;
    top: 0;
    height: 100vh;
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
    padding: var(--space-5) var(--space-3);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: 0 var(--space-3);
    color: var(--ink);
    text-decoration: none;
    font-weight: 600;
    font-size: var(--step-1);
    letter-spacing: -0.01em;
  }
  ul { list-style: none; margin: 0; padding: 0; display: grid; gap: 2px; }
  ul a, .who {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: 8px var(--space-3);
    border-radius: var(--radius-s);
    color: var(--ink-2);
    text-decoration: none;
    font-weight: 500;
  }
  ul a:hover, .who:hover { background: var(--sunken); color: var(--ink); }
  ul a.active, .who.active {
    color: var(--accent);
    background: var(--accent-soft);
  }
  .rail-foot { margin-top: auto; display: grid; gap: var(--space-3); }

  .server-bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    margin-bottom: var(--space-4);
  }
  .server-bar:not(:has(.server-switch)) { justify-content: flex-end; }
  .server-switch {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--ink-2);
    font-size: 14px;
  }
  .add-server { color: var(--ink-2); }
  .bar-links { display: inline-flex; gap: var(--space-4); }
  .stopped-banner { display: flex; justify-content: space-between; align-items: center; gap: var(--space-3); margin-bottom: var(--space-4); }
  .avatar {
    display: grid;
    place-items: center;
    width: 24px;
    height: 24px;
    border-radius: 50%;
    background: var(--sunken);
    color: var(--ink);
    font-size: var(--step--1);
    font-weight: 600;
  }

  main {
    min-width: 0;
    padding: var(--space-6) var(--space-6) var(--space-7);
    max-width: 1240px;
  }

  .toasts {
    position: fixed;
    right: var(--space-5);
    bottom: var(--space-5);
    display: grid;
    gap: var(--space-2);
    z-index: 50;
  }
  .toast {
    padding: var(--space-3) var(--space-4);
    border-radius: var(--radius-s);
    background: var(--ink);
    color: var(--bg);
    box-shadow: var(--shadow);
    font-size: var(--step--1);
    max-width: 360px;
  }
  .toast.bad { background: var(--bad); color: #fff; }

  /* Phones: the rail becomes a bottom tab bar */
  @media (max-width: 760px) {
    .shell { grid-template-columns: 1fr; background: none; }
    .rail {
      position: fixed;
      inset: auto 0 0 0;
      height: auto;
      flex-direction: row;
      padding: 6px var(--space-2) calc(6px + env(safe-area-inset-bottom));
      border-top: 1px solid var(--line);
      background: var(--surface);
      z-index: 40;
    }
    .brand, .rail-foot :global(.theme) { display: none; }
    ul { grid-auto-flow: column; flex: 1; }
    ul a, .who {
      flex-direction: column;
      gap: 2px;
      padding: 6px 4px;
      font-size: 11px;
      justify-content: center;
    }
    .rail-foot { margin: 0; }
    .who span:not(.avatar) { display: none; }
    main { padding: var(--space-5) var(--space-4) 96px; }
    .toasts { left: var(--space-4); right: var(--space-4); bottom: 84px; }
  }
</style>
