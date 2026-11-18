<script>
  import { api } from '../lib/api.js';
  import { bytes, duration } from '../lib/format.js';
  import { route, setQuery } from '../lib/state.svelte.js';
  import BarChart from '../lib/BarChart.svelte';

  const presets = [
    { key: 'last_30_days', label: 'Last 30 days' },
    { key: 'this_month', label: 'This month' },
    { key: 'last_month', label: 'Last month' },
    { key: 'this_year', label: 'This year', group: 'month' },
    { key: 'last_year', label: 'Last year', group: 'month' },
  ];

  // Selection lives in the URL so a report can be bookmarked.
  const q = route.query;
  let preset = $state(q.get('preset') ?? (q.get('from') ? '' : 'last_30_days'));
  let from = $state(q.get('from') ?? '');
  let to = $state(q.get('to') ?? '');
  let group = $state(q.get('group') ?? 'day');
  let name = $state(q.get('name') ?? '');

  let report = $state(null);
  let error = $state('');
  let loading = $state(false);
  let clients = $state([]);

  const params = $derived.by(() => {
    const p = new URLSearchParams();
    if (preset) p.set('preset', preset);
    else {
      p.set('from', from);
      p.set('to', to);
    }
    p.set('group', group);
    if (name) p.set('name', name);
    return p.toString();
  });

  $effect(() => {
    const qs = params;
    if (!preset && (!from || !to)) return;
    setQuery({ preset, from: preset ? '' : from, to: preset ? '' : to, group, name });
    loading = true;
    const ctl = new AbortController();
    api('/stats?' + qs, { signal: ctl.signal })
      .then((r) => {
        report = r;
        error = '';
        if (preset) {
          from = r.from;
          to = r.to;
        }
      })
      .catch((e) => e.name !== 'AbortError' && (error = e.message))
      .finally(() => (loading = false));
    return () => ctl.abort();
  });

  $effect(() => {
    api('/certs')
      .then((cs) => (clients = [...new Set(cs.filter((c) => c.kind !== 'server').map((c) => c.name))].sort()))
      .catch(() => {});
  });

  function pick(p) {
    preset = p.key;
    if (p.group) group = p.group;
    else if (group === 'year') group = 'day';
  }
  function custom() {
    preset = '';
  }

  const total = $derived(report ? report.total.upload + report.total.download : 0);
</script>

<header class="head">
  <div>
    <h1>Traffic</h1>
    <p class="muted">How much each client sent and received, per {group}.</p>
  </div>
  {#if report}
    <a class="btn" href="/api/v1/stats.csv?{params}" download>Export CSV</a>
  {/if}
</header>

<section class="controls" aria-label="Report settings">
  <div class="seg" role="radiogroup" aria-label="Period">
    {#each presets as p (p.key)}
      <button role="radio" aria-checked={preset === p.key} onclick={() => pick(p)}>{p.label}</button>
    {/each}
  </div>
  <div class="range">
    <label class="field">
      <span>From</span>
      <input type="date" bind:value={from} max={to} oninput={custom} />
    </label>
    <label class="field">
      <span>To</span>
      <input type="date" bind:value={to} min={from} oninput={custom} />
    </label>
    <label class="field">
      <span>Group by</span>
      <select bind:value={group}>
        <option value="day">Day</option>
        <option value="month">Month</option>
        <option value="year">Year</option>
      </select>
    </label>
    <label class="field">
      <span>Client</span>
      <select bind:value={name}>
        <option value="">All clients</option>
        {#each clients as c (c)}<option value={c}>{c}</option>{/each}
      </select>
    </label>
  </div>
</section>

{#if error}<div class="banner bad" role="alert">Could not load the report: {error}</div>{/if}

{#if report}
  <section class="totals" class:loading aria-label="Totals">
    <div><span class="k">Downloaded</span><span class="v">{bytes(report.total.download)}</span></div>
    <div><span class="k">Uploaded</span><span class="v">{bytes(report.total.upload)}</span></div>
    <div><span class="k">Sessions</span><span class="v">{report.total.sessions}</span></div>
    <div><span class="k">Online time</span><span class="v">{duration(report.total.online_seconds)}</span></div>
  </section>

  <section class="panel chart" class:loading>
    <div class="panel-body">
      <BarChart series={report.series} group={report.group} />
    </div>
  </section>

  {#if !name}
    <section class="panel">
      <div class="panel-head">
        <h2>By client</h2>
        <span class="faint small">{report.from} to {report.to}, {report.time_zone}</span>
      </div>
      {#if report.users.length === 0}
        <p class="empty">No traffic in this period.</p>
      {:else}
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Client</th>
                <th class="num hide-s">Down</th>
                <th class="num hide-s">Up</th>
                <th class="num">Total</th>
                <th class="share-h">Share</th>
                <th class="num hide-s">Sessions</th>
                <th class="num hide-s">Online</th>
              </tr>
            </thead>
            <tbody>
              {#each report.users as u (u.name)}
                <tr>
                  <td><button class="link" onclick={() => (name = u.name)} title="Show only {u.name}">{u.name}</button></td>
                  <td class="num hide-s">{bytes(u.download)}</td>
                  <td class="num hide-s">{bytes(u.upload)}</td>
                  <td class="num nowrap"><strong>{bytes(u.upload + u.download)}</strong></td>
                  <td class="share">
                    <span class="meter"><i style="width: {u.share * 100}%"></i></span>
                    <span class="small muted">{Math.round(u.share * 100)}%</span>
                  </td>
                  <td class="num hide-s">{u.sessions}</td>
                  <td class="num hide-s">{duration(u.online_seconds)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>
  {:else}
    <p class="back"><button class="link" onclick={() => (name = '')}>Show all clients</button></p>
  {/if}
  {#if total === 0 && !loading}<p class="faint small note">Traffic is recorded while trapdoor runs; earlier periods are empty.</p>{/if}
{/if}

<style>
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-4); margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }

  .controls { display: grid; gap: var(--space-4); margin-bottom: var(--space-5); }
  .seg { display: flex; flex-wrap: wrap; gap: 2px; padding: 2px; background: var(--sunken); border-radius: var(--radius-s); width: fit-content; }
  .seg button {
    height: 30px;
    padding: 0 var(--space-3);
    border: 0;
    border-radius: 3px;
    background: transparent;
    color: var(--ink-2);
    font: inherit;
    font-weight: 500;
    cursor: pointer;
  }
  .seg button:hover { color: var(--ink); }
  .seg button[aria-checked='true'] { background: var(--surface); color: var(--ink); box-shadow: 0 1px 2px rgb(0 0 0 / 12%); }
  .range { display: flex; flex-wrap: wrap; gap: var(--space-3); }
  .range .field { min-width: 140px; }

  .banner { margin-bottom: var(--space-4); }

  .totals {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    margin-bottom: var(--space-4);
    border-top: 1px solid var(--line);
    border-bottom: 1px solid var(--line);
  }
  .totals > div { display: grid; gap: 2px; padding: var(--space-4) var(--space-4) var(--space-4) 0; }
  .totals > div + div { padding-left: var(--space-4); border-left: 1px solid var(--line); }
  .k { font-size: var(--step--1); color: var(--ink-2); }
  .v { font-size: var(--step-2); font-weight: 500; letter-spacing: -0.01em; }

  .chart { margin-bottom: var(--space-5); }
  .loading { opacity: 0.6; transition: opacity 150ms; }

  .link { padding: 0; border: 0; background: none; color: var(--accent); font: inherit; font-weight: 500; cursor: pointer; }
  .link:hover { text-decoration: underline; }
  .share-h { width: 22%; }
  .share { display: flex; align-items: center; gap: var(--space-2); }
  .meter { flex: 1; height: 6px; border-radius: 3px; background: var(--sunken); overflow: hidden; min-width: 60px; }
  .meter i { display: block; height: 100%; background: var(--accent); }
  .back { margin-top: var(--space-2); }
  .nowrap, .link { white-space: nowrap; }
  .note { margin-top: var(--space-3); }

  @media (max-width: 760px) {
    .totals { grid-template-columns: 1fr 1fr; }
    .totals > div:nth-child(3) { padding-left: 0; border-left: 0; }
    .totals > div:nth-child(n + 3) { border-top: 1px solid var(--line); }
    .hide-s { display: none; }
    .range .field { flex: 1 1 40%; min-width: 0; }
  }
</style>
