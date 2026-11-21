<script>
  // Overview of a WireGuard or AmneziaWG server, laid out like OpenVPN's:
  // today's numbers and timeline, who is connected, and what needs attention.
  // The server's own settings are on the Server page.
  import { onMount } from 'svelte';
  import { api, poll } from '../lib/api.js';
  import { money } from '../lib/limits.js';
  import { bytes, duration, relative, date } from '../lib/format.js';
  import { currentServer, servers, settings } from '../lib/state.svelte.js';
  import TodayTrace from '../lib/TodayTrace.svelte';

  let status = $state(null);
  let peers = $state([]);
  let live = $state(null);
  let liveError = $state('');
  let history = $state([]);
  let today = $state(null);
  let cost = $state(null); // this month's traffic cost (prices on the Limits page)
  let now = $state(new Date());

  const srv = $derived(status?.agent?.servers?.find((x) => x.id === servers.current) ?? currentServer());
  const kind = $derived(srv?.type === 'amneziawg' ? 'AmneziaWG' : 'WireGuard');

  async function refresh() {
    now = new Date();
    const [st, ps, hs, rep] = await Promise.allSettled([
      api('/status'),
      api('/peers'),
      api('/history/sessions?preset=today&limit=500'),
      api('/stats?preset=today'),
    ]);
    if (st.status === 'fulfilled') status = st.value;
    if (ps.status === 'fulfilled') peers = ps.value;
    if (hs.status === 'fulfilled') history = hs.value;
    if (rep.status === 'fulfilled') today = rep.value;
    api('/cost').then((c) => (cost = c), () => {});
    try {
      live = await api('/sessions');
      liveError = '';
    } catch (e) {
      liveError = e.message;
    }
  }
  onMount(() => poll(refresh, 15000));

  // Clients whose end date has passed or comes within the warning window.
  const attention = $derived(
    peers
      .filter((p) => !p.disabled && p.expires_at)
      .map((p) => ({ p, days: (new Date(p.expires_at) - now) / 86400000 }))
      .filter(({ p, days }) => p.expired || days <= settings.expiryWarnDays)
      .sort((a, b) => a.days - b.days),
  );
  const enabled = $derived(peers.filter((p) => !p.disabled).length);

  const serverProblem = $derived(
    !status
      ? ''
      : !status.agent_ok
        ? 'The agent is not reachable, so nothing can be changed right now.'
        : srv?.error
          ? `The server has a problem: ${srv.error}`
          : srv && !srv.running
            ? `${kind} is not running, so no one can connect.`
            : '',
  );
</script>

<header class="page-head">
  <h1>Overview</h1>
  {#if srv}
    <p class="muted">{srv.name} · {kind} interface <span class="mono">{srv.id}</span> · {enabled} of {peers.length} {peers.length === 1 ? 'client' : 'clients'} enabled</p>
  {/if}
</header>

{#if serverProblem}<div class="banner bad" role="alert">{serverProblem} <a href="/server">Server</a></div>{/if}

<section class="summary" class:five={cost?.priced} aria-label="Today">
  <div>
    <span class="k">Connected now</span>
    <span class="v">{live ? live.sessions.length : '–'}</span>
  </div>
  <div>
    <span class="k">Downloaded today</span>
    <span class="v">{today ? bytes(today.total.download) : '–'}</span>
  </div>
  <div>
    <span class="k">Uploaded today</span>
    <span class="v">{today ? bytes(today.total.upload) : '–'}</span>
  </div>
  <div>
    <span class="k">Sessions today</span>
    <span class="v">{today ? today.total.sessions : '–'}</span>
  </div>
  {#if cost?.priced}
    <a class="costtile" href="/limits" title="Prices and traffic alarms on the Limits page">
      <span class="k">Cost this month</span>
      <span class="v">{money(cost.cost, cost.currency)}</span>
      {#if cost.projected}<span class="faint small">≈ {money(cost.projected, cost.currency)} by the month's end</span>{/if}
    </a>
  {/if}
</section>

<section class="panel">
  <div class="panel-head">
    <h2>Today</h2>
    <a class="small" href="/statistics?preset=today">Traffic details</a>
  </div>
  <div class="panel-body">
    <TodayTrace sessions={history} {now} />
  </div>
</section>

<div class="cols">
  <section class="panel">
    <div class="panel-head">
      <h2>Connected now</h2>
      <a class="small" href="/sessions">All sessions</a>
    </div>
    {#if liveError}
      <p class="empty">Live sessions are unavailable: {liveError}</p>
    {:else if live && live.sessions.length === 0}
      <p class="empty">No one is connected.</p>
    {:else if live}
      <ul class="list">
        {#each live.sessions as s (s.name)}
          <li>
            <span class="pill ok">{s.name}</span>
            <span class="mono faint">{s.virtual_address}</span>
            <span class="muted small right">{duration((now - new Date(s.connected_since)) / 1000)}, {bytes(s.bytes_sent + s.bytes_received)}</span>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section class="panel">
    <div class="panel-head">
      <h2>Needs attention</h2>
      <a class="small" href="/clients">Clients</a>
    </div>
    {#if attention.length === 0}
      <p class="empty">No client ends in the next {settings.expiryWarnDays} days.</p>
    {:else}
      <ul class="list">
        {#each attention as { p } (p.name)}
          <li>
            <span class="pill {p.expired ? 'bad' : 'warn'}">{p.name}</span>
            <span class="muted small right">{p.expired ? 'Ended' : 'Ends'} {relative(p.expires_at)} ({date(p.expires_at)})</span>
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</div>

<style>
  .page-head { margin-bottom: var(--space-5); }
  .page-head p { margin-top: var(--space-1); }
  .banner { margin-bottom: var(--space-4); }

  .summary {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    margin-bottom: var(--space-5);
    border-top: 1px solid var(--line);
    border-bottom: 1px solid var(--line);
  }
  .summary > div { display: grid; gap: 2px; padding: var(--space-4) var(--space-4) var(--space-4) 0; }
  .summary > div + div { padding-left: var(--space-4); border-left: 1px solid var(--line); }
  .summary.five { grid-template-columns: repeat(5, 1fr); }
  .summary > .costtile { display: grid; gap: 2px; padding: var(--space-4) 0 var(--space-4) var(--space-4); border-left: 1px solid var(--line); color: inherit; text-decoration: none; }
  .summary > .costtile:hover .v { color: var(--accent); }
  .small { font-size: var(--step--1); }
  .k { font-size: var(--step--1); color: var(--ink-2); }
  .v { font-size: var(--step-2); font-weight: 500; letter-spacing: -0.01em; }

  .panel { margin-bottom: var(--space-5); }
  .cols { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-5); }
  .cols .panel { margin-bottom: 0; }

  .list { list-style: none; margin: 0; padding: var(--space-3) var(--space-5) var(--space-4); }
  .list li { display: flex; align-items: center; gap: var(--space-3); padding: 8px 0; border-bottom: 1px solid var(--line); }
  .list li:last-child { border-bottom: 0; }
  .right { margin-left: auto; text-align: right; }

  @media (max-width: 900px) {
    .cols { grid-template-columns: 1fr; }
  }
  @media (max-width: 760px) {
    .summary { grid-template-columns: 1fr 1fr; }
    .summary > div:nth-child(3) { padding-left: 0; border-left: 0; }
    .summary > div:nth-child(n + 3) { border-top: 1px solid var(--line); }
  }
</style>
