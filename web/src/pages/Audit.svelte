<script>
  // The audit log: every change, sign-in and failed sign-in, who did it,
  // how they signed in and from where. Admins only; kept two years.
  import { api, apiURL } from '../lib/api.js';
  import { route, setQuery } from '../lib/state.svelte.js';
  import { dateTime } from '../lib/format.js';
  import { savedSize } from '../lib/pager.js';
  import { actionText, actionTone, actionGroups, periodFrom } from '../lib/audit.js';
  import AdminNav from '../lib/AdminNav.svelte';
  import Pager from '../lib/Pager.svelte';

  const q = route.query;
  let period = $state(q.get('period') ?? '30d');
  let user = $state(q.get('user') ?? '');
  let action = $state(q.get('action') ?? '');
  let search = $state(q.get('q') ?? '');
  let page = $state(Math.max(1, Number(q.get('page')) || 1));
  let size = $state(savedSize());
  let data = $state(null);
  let error = $state('');
  let loading = $state(false);

  const params = $derived.by(() => {
    const p = new URLSearchParams();
    const from = periodFrom(period);
    if (from) p.set('from', from);
    if (user) p.set('user', user);
    if (action) p.set('action', action);
    if (search.trim()) p.set('q', search.trim());
    return p;
  });

  // Filters changed: back to the first page.
  let lastFilter = '';
  $effect(() => {
    const key = params.toString();
    if (lastFilter && key !== lastFilter) page = 1;
    lastFilter = key;
  });

  let seq = 0;
  $effect(() => {
    const p = new URLSearchParams(params);
    p.set('offset', String((page - 1) * size));
    p.set('limit', String(size));
    setQuery({ period: period === '30d' ? '' : period, user, action, q: search.trim(), page: page > 1 ? page : '' });
    const n = ++seq;
    loading = true;
    const t = setTimeout(async () => {
      try {
        const d = await api(`/audit?${p}`);
        if (n === seq) {
          data = d;
          error = '';
        }
      } catch (e) {
        if (n === seq) error = e.message;
      } finally {
        if (n === seq) loading = false;
      }
    }, search ? 250 : 0);
    return () => clearTimeout(t);
  });

  const info = $derived.by(() => {
    const total = data?.total ?? 0;
    const pages = Math.max(1, Math.ceil(total / size));
    return { total, page: Math.min(page, pages), pages, from: total ? (page - 1) * size + 1 : 0, to: Math.min(page * size, total) };
  });
  const csvURL = $derived(apiURL(`/audit.csv?${params}`));
  const viaText = { password: 'password', oidc: 'single sign-on', token: 'API token' };
</script>

<AdminNav />
<header class="head">
  <div>
    <h1>Audit log</h1>
    <p class="muted">Every change, sign-in and failed sign-in: who, how they signed in, from where. Kept for two years.</p>
  </div>
  <a class="btn" href={csvURL} download>Export CSV</a>
</header>

<section class="filters" aria-label="Filters">
  <label class="field"><span>Period</span>
    <select bind:value={period}>
      <option value="today">Today</option>
      <option value="7d">Last 7 days</option>
      <option value="30d">Last 30 days</option>
      <option value="1y">Last year</option>
      <option value="all">Everything</option>
    </select>
  </label>
  <label class="field"><span>Who</span>
    <select bind:value={user}>
      <option value="">Everyone</option>
      {#each data?.users ?? [] as u (u)}<option value={u}>{u}</option>{/each}
    </select>
  </label>
  <label class="field"><span>What</span>
    <select bind:value={action}>
      {#each actionGroups as [v, l] (v)}<option value={v}>{l}</option>{/each}
    </select>
  </label>
  <label class="field grow"><span>Search</span>
    <input type="search" bind:value={search} placeholder="a client, server, address…" autocomplete="off" />
  </label>
</section>

{#if error}<div class="banner bad" role="alert">{error}</div>{/if}

{#if data}
  <section class="panel" class:loading>
    {#if data.entries.length === 0}
      <p class="panel-body muted">Nothing in this period{user || action || search ? ' with these filters' : ''}.</p>
    {:else}
      <div class="table-wrap">
        <table class="cards">
          <thead><tr><th>When</th><th>Who</th><th>What</th><th>Server</th><th>From</th></tr></thead>
          <tbody>
            {#each data.entries as e (e.id)}
              <tr>
                <td data-label="When" class="nowrap">{dateTime(e.time)}</td>
                <td data-label="Who"><strong>{e.username || '–'}</strong>{#if e.via}<span class="faint small block">{viaText[e.via] ?? e.via}</span>{/if}</td>
                <td data-label="What">
                  <span class="act {actionTone(e.action)}">{actionText(e.action)}</span>
                  {#if e.target}<span class="mono small target">{e.target}</span>{/if}
                </td>
                <td data-label="Server" class="mono small">{e.server || '–'}</td>
                <td data-label="From" class="mono small">{e.ip || '–'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <Pager bind:page bind:size {info} label="Pages of the audit log" />
    {/if}
  </section>
{/if}

<style>
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-4); flex-wrap: wrap; margin-bottom: var(--space-4); }
  .head p { margin-top: var(--space-1); }
  .filters { display: flex; gap: var(--space-3); flex-wrap: wrap; align-items: flex-end; margin-bottom: var(--space-4); }
  .filters .field { min-width: 150px; }
  .filters .grow { flex: 1 1 220px; }
  .banner { margin-bottom: var(--space-4); }
  .loading { opacity: 0.6; transition: opacity 0.2s; }
  .nowrap { white-space: nowrap; }
  .block { display: block; }
  .small { font-size: var(--step--1); }
  .act { font-weight: 500; }
  .act.bad { color: var(--bad); }
  .act.warn { color: var(--warn); }
  .target { margin-left: var(--space-2); color: var(--ink-2); overflow-wrap: anywhere; }
  @media (max-width: 640px) {
    .filters .field { flex: 1 1 140px; min-width: 0; }
    .cards thead { display: none; }
    .cards, .cards tbody, .cards tr, .cards td { display: block; width: 100%; }
    .cards tr { padding: var(--space-3) var(--space-4); border-top: 1px solid var(--line); }
    .cards td { padding: 2px 0; border: 0; display: grid; grid-template-columns: 70px minmax(0, 1fr); column-gap: var(--space-2); align-items: baseline; }
    .cards td[data-label]::before { content: attr(data-label); color: var(--ink-3); font-size: var(--step--1); }
    .cards td > * { grid-column: 2; justify-self: start; }
    .target { margin-left: 0; }
  }
</style>
