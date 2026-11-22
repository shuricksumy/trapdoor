<script>
  import { readOnly, route, session, navigate, toasts, toast, settings, servers, setServers, selectServer, currentServer, repoURL, versionURL, shortVersion } from './lib/state.svelte.js';
  import { api, reloadServers } from './lib/api.js';
  import { machines, typeNames, where } from './lib/location.js';

  // Pages that do not belong to the selected server.
  const serverFree = ['/servers', '/servers/new', '/nodes', '/hosts', '/account', '/admins', '/backups', '/notifications', '/audit'];
  import ThemeSwitch from './lib/ThemeSwitch.svelte';
  import Login from './pages/Login.svelte';
  import Dashboard from './pages/Dashboard.svelte';
  import Certificates from './pages/Certificates.svelte';
  import Sessions from './pages/Sessions.svelte';
  import Statistics from './pages/Statistics.svelte';
  import Uptime from './pages/Uptime.svelte';
  import Tools from './pages/Tools.svelte';
  import Limits from './pages/Limits.svelte';
  import Account from './pages/Account.svelte';
  import Admins from './pages/Admins.svelte';
  import Backups from './pages/Backups.svelte';
  import Notifications from './pages/Notifications.svelte';
  import Audit from './pages/Audit.svelte';
  import Server from './pages/Server.svelte';
  import Setup from './pages/Setup.svelte';
  import Access from './pages/Access.svelte';
  import WireGuardOverview from './pages/WireGuardOverview.svelte';
  import WireGuardClients from './pages/WireGuardClients.svelte';
  import NewServer from './pages/NewServer.svelte';
  import Nodes from './pages/Nodes.svelte';
  import Servers from './pages/Servers.svelte';
  import Network from './pages/Network.svelte';
  import WireGuardServer from './pages/WireGuardServer.svelte';

  const pages = [
    { path: '/', label: 'Overview', icon: 'M3 11l9-7 9 7v9a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z', component: Dashboard },
    { path: '/clients', label: 'Clients', icon: 'M12 3l7 3v5c0 4.5-3 8.5-7 10-4-1.5-7-5.5-7-10V6z', component: Certificates },
    { path: '/sessions', label: 'Connected', icon: 'M4 12h4l2-5 4 10 2-5h4', component: Sessions },
    { path: '/uptime', label: 'Uptime', icon: 'M3 12h3l2-4 3 8 2-4h3M17 6h4v4M21 6l-4 4M3 19h18', component: Uptime },
    { path: '/statistics', label: 'Traffic', icon: 'M5 20V11M10 20V5M15 20v-7M20 20V8', component: Statistics },
    { path: '/limits', label: 'Limits', icon: 'M4.5 17a8.5 8.5 0 1 1 15 0M12 13l4-4M12 13h.01', component: Limits },
    { path: '/network', label: 'Network', icon: 'M12 3v6M5 21v-4h14v4M12 9a2 2 0 1 0 0 .01M12 11v6M5 17v-2h14v2', component: Network },
    { path: '/access', label: 'Access', icon: 'M7 11V8a5 5 0 0 1 10 0v3M5 11h14v10H5zM12 15v2', component: Access },
    { path: '/tools', label: 'Tools', icon: 'M14.7 6.3a4 4 0 0 0-5.4 5.2L3 17.8V21h3.2l6.3-6.3a4 4 0 0 0 5.2-5.4l-2.6 2.6-2.4-.6-.6-2.4z', component: Tools },
    { path: '/server', label: 'Server', icon: 'M4 5h16v5H4zM4 14h16v5H4zM8 7.5h.01M8 16.5h.01', component: Server },
  ];

  const wgPages = [
    { path: '/', label: 'Overview', icon: pages[0].icon, component: WireGuardOverview },
    { path: '/clients', label: 'Clients', icon: pages[1].icon, component: WireGuardClients },
    // Connected, Uptime, Traffic, Limits, Network, Access: the same pages
    ...pages.filter((p) => ['/sessions', '/uptime', '/statistics', '/limits', '/network', '/access'].includes(p.path)),
    pages.find((p) => p.path === '/tools'),
    { path: '/server', label: 'Server', icon: pages.find((p) => p.path === '/server').icon, component: WireGuardServer },
  ];

  // WireGuard and AmneziaWG servers share their pages.
  const isWG = $derived(['wireguard', 'amneziawg'].includes(currentServer()?.type));
  // A read-only account runs no tools (they act on the server's network).
  const navPages = $derived((isWG ? wgPages : pages).filter((p) => !(readOnly() && p.path === '/tools')));

  const current = $derived(
    route.path === '/login'
      ? { component: Login }
      : route.path === '/account'
        ? { component: Account }
        : route.path === '/admins'
          ? { component: Admins }
        : route.path === '/backups'
          ? { component: Backups }
        : route.path === '/notifications'
          ? { component: Notifications }
        : route.path === '/audit'
          ? { component: Audit }
        : route.path === '/servers/new'
          ? { component: NewServer }
          : route.path === '/nodes' || route.path === '/hosts'
            ? { component: Nodes }
            : route.path === '/servers'
              ? { component: Servers }
          : route.path === '/setup' && !isWG
            ? { component: Setup }
            : (navPages.find((p) => p.path === route.path) ?? navPages[0]),
  );

  // Old address of the OpenVPN clients page.
  $effect(() => {
    if (route.path === '/certificates') navigate('/clients' + location.search, { replace: true });
  });

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
        settings.version = st.web_version ?? '';
        setServers(st.agent?.servers ?? [], st.agent?.hosts ?? [], st.agent?.containers ?? null, st.agent ?? {});
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
        if (st.needed && (route.path === '/' || route.path === '/statistics')) navigate('/setup', { replace: true });
      })
      .catch(() => {});
  });

  $effect(() => {
    const titles = { '/login': 'Log in', '/account': 'My account', '/admins': 'Admins', '/backups': 'Backups', '/notifications': 'Notifications', '/audit': 'Audit log', '/setup': 'Setup', '/servers/new': 'Add a server', '/nodes': 'Nodes', '/hosts': 'Nodes', '/servers': 'Servers' };
    const name = titles[route.path] ?? navPages.find((p) => p.path === route.path)?.label ?? 'Overview';
    document.title = `${name} · Trapdoor`;
  });
</script>

{#if !session.checked}
  <div class="boot" aria-busy="true"></div>
{:else if route.path === '/login' || !session.user}
  <Login />
{:else}
  <div class="shell">
    <nav class="rail" aria-label="Main">
      <a class="brand" href="/" aria-label="Trapdoor overview">
        <svg viewBox="0 0 32 32" width="26" height="26" aria-hidden="true">
          <path d="M5 22c0-6 5-11 11-11s11 5 11 11" fill="none" stroke="var(--accent)" stroke-width="3" stroke-linecap="round" />
          <path d="M10.5 22c0-3 2.5-5.5 5.5-5.5s5.5 2.5 5.5 5.5" fill="none" stroke="var(--upload)" stroke-width="3" stroke-linecap="round" />
        </svg>
        <span>Trapdoor</span>
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
        <a class="who" href="/account" class:active={serverFree.includes(route.path)} title="Admin: your account, admins, servers, nodes, backups, notifications">
          <span class="avatar" aria-hidden="true">{session.user.username.slice(0, 1).toUpperCase()}</span>
          <span>{session.user.username}</span>
        </a>
        <p class="about">
          {#if settings.version}<a href={versionURL(settings.version)} target="_blank" rel="noopener noreferrer" title={settings.version}>{shortVersion(settings.version)}</a><span aria-hidden="true">·</span>{/if}<a href={repoURL} target="_blank" rel="noopener noreferrer">GitHub</a>
        </p>
      </div>
    </nav>

    <main class:readonly={readOnly()}>
      {#if readOnly()}<div class="banner readonly-note" role="status">Read-only account: you can look around; only admins can change things.</div>{/if}
      {#if !serverFree.includes(route.path)}
      <div class="server-bar">
        <div class="server-pick">
        {#if servers.list.length > 1}
          <label class="server-switch">
            <span>Server</span>
            <select value={servers.current} onchange={(e) => { selectServer(e.currentTarget.value); navigate('/'); }}>
              {#if servers.hosts.length}
                {#each machines().flatMap((m) => m.nodes) as g (g.key)}
                  {@const list = servers.list.filter((sv) => (sv.host ?? '') === g.key)}
                  {#if list.length}
                    <optgroup label="{g.machine} › node {g.name}">
                      {#each list as sv (sv.id)}<option value={sv.id}>{sv.name} · {g.name}</option>{/each}
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
        {#if currentServer() && !serverFree.includes(route.path)}
          <!-- Which server the page acts on, and where it runs. -->
          <span class="where-chip" title="The page changes this server">
            <strong>{typeNames[currentServer().type] ?? currentServer().type}</strong>
            <span>{where(currentServer())}</span>
          </span>
        {/if}
        </div>
        <!-- The way to the Admin area: servers, nodes, admins, backups, notifications. -->
        <a class="manage" href="/servers" title="Servers, nodes, admins, backups and notifications">
          <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true"><path d="M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>
          <span>Manage</span>
        </a>
      </div>
      {/if}
      {#if currentServer()?.stopped && !serverFree.includes(route.path)}
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
  .about { display: flex; gap: 6px; white-space: nowrap; margin: var(--space-2) 0 0; padding: 0 var(--space-3); font-size: var(--step--1); color: var(--ink-3, var(--ink-2)); }
  .about a { color: inherit; text-decoration: none; }
  .about a:hover { color: var(--accent); text-decoration: underline; }
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
    margin-bottom: var(--space-5);
    padding: var(--space-3) var(--space-4);
    border: 1px solid var(--line);
    border-radius: var(--radius-m);
    background: var(--sunken);
  }
  .server-bar select { background-color: var(--surface); }
  .server-bar:not(:has(.server-switch, .where-chip)) { justify-content: flex-end; }
  .readonly-note { margin-bottom: var(--space-4); background: var(--sunken); color: var(--ink-2); }
  .manage { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 var(--space-3); border: 1px solid var(--line); border-radius: var(--radius-s); background: var(--surface); color: var(--ink-2); font-size: var(--step--1); font-weight: 500; text-decoration: none; white-space: nowrap; }
  .manage:hover { border-color: var(--accent); color: var(--ink); }
  .server-pick { display: inline-flex; align-items: center; gap: var(--space-3); flex-wrap: wrap; min-width: 0; }
  .where-chip {
    display: inline-flex; align-items: center; gap: var(--space-2); padding: 3px 10px;
    border: 1px solid var(--line); border-radius: 999px; background: var(--surface);
    font-size: var(--step--1); color: var(--ink-2); max-width: 100%;
  }
  .where-chip strong { color: var(--ink); font-weight: 600; }
  .where-chip span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .server-switch {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--ink-2);
    font-size: 14px;
  }
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
      padding: 6px 4px calc(6px + env(safe-area-inset-bottom));
      border-top: 1px solid var(--line);
      background: var(--surface);
      z-index: 40;
    }
    .brand, .about, .rail-foot :global(.theme) { display: none; }
    /* Up to 7 items: equal columns that may shrink; the account stays visible. */
    ul { grid-auto-flow: column; grid-auto-columns: minmax(0, 1fr); flex: 1; min-width: 0; }
    ul a, .who {
      flex-direction: column;
      gap: 2px;
      padding: 6px 2px;
      font-size: 10px;
      letter-spacing: -0.2px;
      justify-content: center;
      min-width: 0;
    }
    ul a span { max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .rail-foot { margin: 0; flex-shrink: 0; }
    .who span:not(.avatar) { display: none; }
    main { padding: var(--space-5) var(--space-4) 96px; }
    .toasts { left: var(--space-4); right: var(--space-4); bottom: 84px; }
  }
</style>
