<script>
  // Uptime of the selected server's clients over any period: a summary, a
  // status bar per client (by hour up to 3 days, by day beyond), a calendar of
  // one client, and every up and down in time order. Filters live in the URL.
  import { api, apiURL } from '../lib/api.js';
  import { currentServer, route, setQuery } from '../lib/state.svelte.js';
  import { clientName, loadClientNames } from '../lib/names.svelte.js';
  import { duration, relative, dateTime } from '../lib/format.js';
  import { bucketSpan, level, monthGrid, preset, presetOf, sortClients, uptimePct } from '../lib/uptime.js';

  const isWG = ['wireguard', 'amneziawg'].includes(currentServer()?.type);
  const presets = [['today', 'Today'], ['7d', '7 days'], ['30d', '30 days'], ['this_month', 'This month'], ['last_month', 'Last month'], ['this_year', 'This year']];

  const q = route.query;
  const start = preset('7d');
  let from = $state(q.get('from') ?? start.from);
  let to = $state(q.get('to') ?? start.to);
  let name = $state(q.get('name') ?? '');
  let quiet = $state(q.get('quiet') !== '0');
  const picked = $derived(presetOf(from, to));
  function pick(k) {
    const p = preset(k);
    from = p.from;
    to = p.to;
  }

  let report = $state(null);
  let known = $state([]); // every current client, also the ones never seen
  let events = $state([]);
  let total = $state(0);
  let error = $state('');
  let loading = $state(false);
  let now = $state(new Date());

  const params = $derived(new URLSearchParams({ from, to, ...(name ? { name } : {}), quiet: quiet ? '1' : '0' }).toString());

  $effect(() => {
    loadClientNames();
    (isWG ? api('/peers') : api('/certs'))
      .then((list) => (known = isWG ? list.filter((p) => !p.disabled).map((p) => p.name) : list.filter((c) => c.current && c.kind !== 'server' && c.status !== 'revoked').map((c) => c.name)))
      .catch(() => {});
  });
  $effect(() => {
    const qs = params;
    if (!from || !to || to < from) return;
    setQuery({ from, to, name, quiet: quiet ? '' : '0' });
    loading = true;
    now = new Date();
    Promise.all([api(`/history/uptime?from=${from}&to=${to}`), api(`/history/events?${qs}&limit=100`)])
      .then(([u, e]) => {
        report = u;
        events = e.events;
        total = e.total;
        error = '';
      })
      .catch((e) => (error = e.message))
      .finally(() => (loading = false));
  });
  async function more() {
    try {
      const e = await api(`/history/events?${params}&limit=100&offset=${events.length}`);
      events = [...events, ...e.events];
      total = e.total;
    } catch (e) {
      error = e.message;
    }
  }

  // Status bars
  const rows = $derived.by(() => {
    if (!report) return [];
    const by = new Map(report.clients.map((c) => [c.name, c]));
    for (const n of known) if (!by.has(n)) by.set(n, { name: n, online: false, last_seen: null, sessions: 0, online_seconds: 0, buckets: report.starts.map(() => 0) });
    const list = sortClients([...by.values()], clientName);
    return name ? list.filter((c) => c.name === name) : list;
  });
  const spans = $derived(report ? report.starts.map((s, i) => bucketSpan(s, report.step, now, report.starts[i + 1] ?? report.to)) : []);
  const fmtStart = (s) =>
    report.step === 'day'
      ? new Date(s).toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' })
      : new Date(s).toLocaleString([], { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
  const tip = (c, i) => `${fmtStart(report.starts[i])}: ${spans[i] <= 0 ? 'not yet' : c.buckets[i] ? `online ${duration(c.buckets[i])}` : 'offline'}`;

  // Summary of the period
  const summary = $derived.by(() => {
    if (!report) return null;
    const seen = rows.filter((c) => c.sessions > 0 || c.online_seconds > 0);
    const pcts = rows.map((c) => uptimePct(c.online_seconds, report, now));
    return {
      online: rows.filter((c) => c.online).length,
      clients: rows.length,
      active: seen.length,
      sessions: rows.reduce((a, c) => a + c.sessions, 0),
      time: rows.reduce((a, c) => a + c.online_seconds, 0),
      avg: pcts.length ? Math.round((pcts.reduce((a, b) => a + b, 0) / pcts.length) * 10) / 10 : 0,
    };
  });
  // the period's labels, e.g. "Nov 14 – Nov 20, 2026"
  const label = $derived.by(() => {
    const a = new Date(from + 'T12:00:00');
    const b = new Date(to + 'T12:00:00');
    const o = { day: 'numeric', month: 'short', year: 'numeric' };
    return from === to ? a.toLocaleDateString([], o) : `${a.toLocaleDateString([], { day: 'numeric', month: 'short' })} – ${b.toLocaleDateString([], o)}`;
  });

  // Calendar of one client
  let cal = $state(null); // { name, y, m, report, day }
  function openCal(n) {
    const d = new Date(to + 'T12:00:00');
    cal = { name: n, y: d.getFullYear(), m: d.getMonth() + 1, report: null, day: null };
  }
  function shift(k) {
    const d = new Date(cal.y, cal.m - 1 + k, 1);
    cal = { ...cal, y: d.getFullYear(), m: d.getMonth() + 1, report: null, day: null };
  }
  const mm = (n) => String(n).padStart(2, '0');
  $effect(() => {
    if (!cal || cal.report) return;
    const key = `${cal.y}-${mm(cal.m)}`;
    api(`/history/uptime?month=${key}`)
      .then((r) => cal && (cal = { ...cal, report: r }))
      .catch((e) => (error = e.message));
  });
  const mine = $derived(cal?.report?.clients.find((c) => c.name === cal.name) ?? null);
  const busiest = $derived(Math.max(1, ...(mine?.buckets ?? [0])));
  const shade = (sec) => (sec <= 0 ? 0 : Math.max(0.15, Math.min(1, sec / busiest)));
  const isFuture = (d) => new Date(cal.y, cal.m - 1, d) > now;
  async function openDay(d) {
    const date = `${cal.y}-${mm(cal.m)}-${mm(d)}`;
    cal = { ...cal, day: { date, events: null } };
    try {
      const e = await api(`/history/events?from=${date}&to=${date}&name=${encodeURIComponent(cal.name)}&quiet=${quiet ? 1 : 0}&limit=200`);
      cal = { ...cal, day: { date, events: e.events } };
    } catch (err) {
      cal = { ...cal, day: { date, events: [] } };
    }
  }
</script>

<header class="head">
  <div>
    <h1>Uptime</h1>
    <p class="muted">When each client was online: any period, back to the first record.</p>
  </div>
  <a class="btn" href={apiURL(`/history/events.csv?${params}`)} download>Export CSV</a>
</header>

<section class="filters" aria-label="Filters">
  <div class="seg" role="radiogroup" aria-label="Period">
    {#each presets as [k, l] (k)}<button role="radio" aria-checked={picked === k} onclick={() => pick(k)}>{l}</button>{/each}
  </div>
  <div class="row">
    <label class="field"><span>From</span><input type="date" bind:value={from} max={to} /></label>
    <label class="field"><span>To</span><input type="date" bind:value={to} min={from} /></label>
    <label class="field">
      <span>Client</span>
      <select bind:value={name}>
        <option value="">All clients</option>
        {#each [...known].sort((a, b) => clientName(a).localeCompare(clientName(b))) as n (n)}<option value={n}>{clientName(n)}</option>{/each}
      </select>
    </label>
    <label class="check" title="A disconnect followed by a reconnect within the quiet time of the online / offline messages counts as one session">
      <input type="checkbox" bind:checked={quiet} /><span>Hide quick reconnects</span>
    </label>
  </div>
</section>

{#if error}<div class="banner bad" role="alert">{error}</div>{/if}

{#if summary}
  <section class="tiles" class:loading aria-label="Summary of {label}">
    <div><span class="k">Online now</span><span class="v">{summary.online}<small> / {summary.clients}</small></span></div>
    <div><span class="k">Average uptime</span><span class="v">{summary.avg}%</span></div>
    <div><span class="k">Time online</span><span class="v">{duration(summary.time)}</span></div>
    <div><span class="k">Sessions</span><span class="v">{summary.sessions}</span></div>
    <div><span class="k">Ups and downs</span><span class="v">{total}</span></div>
  </section>
{/if}

{#if cal}
  <section class="panel cal">
    <div class="panel-head">
      <h2>{clientName(cal.name)}</h2>
      <div class="nav">
        <button class="btn small ghost" onclick={() => shift(-1)} aria-label="Previous month">←</button>
        <span class="month">{new Date(cal.y, cal.m - 1, 1).toLocaleDateString([], { month: 'long', year: 'numeric' })}</span>
        <button class="btn small ghost" onclick={() => shift(1)} aria-label="Next month" disabled={new Date(cal.y, cal.m, 1) > now}>→</button>
        <button class="btn small" onclick={() => (cal = null)}>Close</button>
      </div>
    </div>
    <div class="panel-body">
      <div class="grid" role="grid" aria-label="Hours online per day">
        {#each ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as w (w)}<span class="wd">{w}</span>{/each}
        {#each monthGrid(cal.y, cal.m) as week, wi (wi)}
          {#each week as cell, ci (ci)}
            {#if cell}
              {@const sec = mine?.buckets[cell.i] ?? 0}
              <button class="day" class:sel={cal.day?.date.endsWith(`-${mm(cell.day)}`)} disabled={isFuture(cell.day)} style="--a: {shade(sec)}"
                onclick={() => openDay(cell.day)} title={sec ? `online ${duration(sec)}` : 'offline'}>
                <span class="n">{cell.day}</span><span class="h">{sec ? duration(sec) : ''}</span>
              </button>
            {:else}<span></span>{/if}
          {/each}
        {/each}
      </div>
      <p class="hint">{new Date(cal.y, cal.m - 1, 1).toLocaleDateString([], { month: 'long' })}: online {duration(mine?.online_seconds ?? 0)} in {mine?.sessions ?? 0} sessions. Pick a day for its ups and downs.</p>
      {#if cal.day}
        <h3>{new Date(cal.day.date + 'T12:00:00').toLocaleDateString([], { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' })}</h3>
        {#if !cal.day.events}<p class="muted">Loading…</p>
        {:else if !cal.day.events.length}<p class="muted">No ups or downs that day.</p>
        {:else}
          <ul class="mini">
            {#each [...cal.day.events].reverse() as e, i (i)}
              <li><span class="mono">{new Date(e.time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</span>
                <span class="ev {e.up ? 'up' : 'down'}">{e.up ? 'up' : 'down'}</span>
                <span class="faint">{e.up ? (e.from ? `from ${e.from}` : '') : `was up ${duration(e.lasted)}`}</span></li>
            {/each}
          </ul>
        {/if}
      {/if}
    </div>
  </section>
{/if}

<section class="panel" class:loading>
  <div class="panel-head"><h2>Status</h2><span class="faint small">{label}{report ? ` · by ${report.step}` : ''}</span></div>
  {#if !report}
    <p class="panel-body muted">Loading…</p>
  {:else if rows.length === 0}
    <p class="panel-body muted">No clients.</p>
  {:else}
    <ul class="rows">
      {#each rows as c (c.name)}
        <li>
          <button class="who" onclick={() => openCal(c.name)} title="Calendar of {clientName(c.name)}">
            <i class="dot" class:on={c.online}></i><span class="name">{clientName(c.name)}</span>
          </button>
          <div class="bars" role="img" aria-label="{clientName(c.name)}: online {uptimePct(c.online_seconds, report, now)}% of the period">
            {#each c.buckets as sec, i (i)}<i class="b {level(sec, spans[i])}" title={tip(c, i)}></i>{/each}
          </div>
          <span class="pct">{uptimePct(c.online_seconds, report, now)}%</span>
          <span class="seen faint small">{c.online ? 'online now' : c.last_seen ? `seen ${relative(c.last_seen)}` : 'never seen'}</span>
        </li>
      {/each}
    </ul>
    <p class="panel-body hint legend"><i class="b full"></i> online <i class="b part"></i> part of the {report.step} <i class="b none"></i> offline · click a client for its calendar</p>
  {/if}
</section>

<section class="panel" class:loading>
  <div class="panel-head"><h2>Ups and downs</h2><span class="faint small">{total} in {label}{quiet ? ', quick reconnects hidden' : ''}</span></div>
  {#if events.length === 0}
    <p class="panel-body muted">{report ? 'None in this period.' : 'Loading…'}</p>
  {:else}
    <div class="table-wrap">
      <table class="events">
        <thead><tr><th>When</th><th>Client</th><th>Event</th><th>Details</th></tr></thead>
        <tbody>
          {#each events as e, i (i)}
            <tr>
              <td class="mono when" data-label="When">{dateTime(e.time)}</td>
              <td data-label="Client"><button class="link" onclick={() => (name = e.name)} title="Only {clientName(e.name)}">{clientName(e.name)}</button></td>
              <td data-label="Event"><span class="ev {e.up ? 'up' : 'down'}">{e.up ? '● up' : '● down'}</span></td>
              <td class="faint" data-label="Details">
                {#if e.up}{e.from ? `from ${e.from}` : ''}{#if e.merged}{e.from ? ' · ' : ''}{e.merged} quick reconnect{e.merged === 1 ? '' : 's'} hidden{/if}
                {:else}was up {duration(e.lasted)}{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if events.length < total}
      <div class="panel-body more"><button class="btn" onclick={more}>Show more ({total - events.length} left)</button></div>
    {/if}
  {/if}
</section>

<style>
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-3); flex-wrap: wrap; margin-bottom: var(--space-4); }
  .head p { margin-top: var(--space-1); }
  .filters { display: grid; gap: var(--space-3); margin-bottom: var(--space-4); }
  .filters .row { display: flex; gap: var(--space-4); align-items: flex-end; flex-wrap: wrap; }
  .filters .field { min-width: 150px; }
  .check { display: flex; gap: var(--space-2); align-items: center; height: 38px; }
  .check input { height: auto; }
  .seg { display: inline-flex; flex-wrap: wrap; gap: 2px; padding: 2px; border-radius: var(--radius-s); background: var(--sunken); justify-self: start; }
  .seg button { height: 30px; padding: 0 var(--space-3); border: 0; border-radius: var(--radius-s); background: transparent; color: var(--ink-2); font: inherit; cursor: pointer; }
  .seg button[aria-checked='true'] { background: var(--surface); color: var(--ink); box-shadow: var(--shadow, 0 1px 2px rgb(0 0 0 / 0.1)); }
  .banner { margin-bottom: var(--space-4); }
  .loading { opacity: 0.6; transition: opacity 0.2s; }
  .tiles { display: grid; grid-template-columns: repeat(5, 1fr); margin-bottom: var(--space-5); border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); }
  .tiles > div { display: grid; gap: 2px; padding: var(--space-4) var(--space-4) var(--space-4) 0; }
  .tiles > div + div { padding-left: var(--space-4); border-left: 1px solid var(--line); }
  .k { font-size: var(--step--1); color: var(--ink-2); }
  .v { font-size: var(--step-2); font-weight: 500; letter-spacing: -0.01em; }
  .v small { font-size: var(--step-0); color: var(--ink-3); }
  .panel { margin-bottom: var(--space-5); }
  .panel-head { display: flex; justify-content: space-between; align-items: center; gap: var(--space-3); flex-wrap: wrap; }
  .rows { list-style: none; margin: 0; padding: 0 var(--space-5) var(--space-3); }
  .rows li { display: grid; grid-template-columns: 200px minmax(0, 1fr) 64px 120px; gap: var(--space-3); align-items: center; padding: 10px 0; border-top: 1px solid var(--line); }
  .rows li:first-child { border-top: 0; }
  .who { display: flex; align-items: center; gap: 8px; min-width: 0; padding: 0; border: 0; background: none; color: var(--ink); font: inherit; font-weight: 600; cursor: pointer; text-align: left; }
  .who:hover .name { text-decoration: underline; }
  .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ink-3); opacity: 0.6; flex: none; }
  .dot.on { background: var(--ok); opacity: 1; }
  .bars { display: flex; gap: 1px; height: 26px; }
  .b { flex: 1; min-width: 1px; border-radius: 2px; }
  .b.full { background: var(--ok); }
  .b.part { background: color-mix(in srgb, var(--ok) 45%, var(--sunken)); }
  .b.none { background: var(--sunken); box-shadow: inset 0 0 0 1px var(--line); }
  .b.future { background: transparent; box-shadow: inset 0 0 0 1px var(--line); opacity: 0.4; }
  .pct { text-align: right; font-variant-numeric: tabular-nums; font-weight: 600; }
  .seen { text-align: right; }
  .legend { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
  .legend .b { display: inline-block; flex: none; width: 10px; height: 12px; }
  .events .when { white-space: nowrap; }
  .ev { display: inline-block; padding: 1px 8px; border-radius: 999px; font-size: var(--step--1); font-weight: 600; white-space: nowrap; }
  .ev.up { color: var(--ok); background: var(--ok-soft); }
  .ev.down { color: var(--bad); background: var(--bad-soft); }
  .link { padding: 0; border: 0; background: none; color: var(--ink); font: inherit; font-weight: 500; cursor: pointer; text-align: left; }
  .link:hover { text-decoration: underline; }
  .more { display: flex; justify-content: center; }
  .cal .nav { display: flex; align-items: center; gap: var(--space-2); }
  .month { min-width: 140px; text-align: center; font-weight: 600; }
  .grid { display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); gap: 6px; max-width: 640px; }
  .wd { font-size: var(--step--1); color: var(--ink-3); text-align: center; }
  .day { display: grid; gap: 2px; padding: 6px; min-height: 52px; border: 1px solid var(--line); border-radius: var(--radius-s); color: var(--ink); font: inherit; text-align: left; cursor: pointer;
    background: color-mix(in srgb, var(--ok) calc(var(--a) * 70%), var(--sunken)); }
  .day:disabled { opacity: 0.35; cursor: default; }
  .day.sel { box-shadow: 0 0 0 2px var(--accent); }
  .day .n { font-weight: 600; font-size: var(--step--1); }
  .day .h { font-size: 11px; }
  h3 { margin: var(--space-4) 0 var(--space-2); font-size: var(--step-0); }
  .mini { list-style: none; margin: 0; padding: 0; display: grid; gap: 4px; }
  .mini li { display: flex; gap: var(--space-2); align-items: center; flex-wrap: wrap; }
  @media (max-width: 900px) {
    .tiles { grid-template-columns: repeat(3, 1fr); }
    .tiles > div:nth-child(4) { padding-left: 0; border-left: 0; }
    .tiles > div:nth-child(n + 4) { border-top: 1px solid var(--line); }
  }
  @media (max-width: 760px) {
    .rows li { grid-template-columns: minmax(0, 1fr) auto; }
    .bars { grid-column: 1 / -1; order: 3; }
    .seen { display: none; }
    .tiles { grid-template-columns: 1fr 1fr; }
    .tiles > div:nth-child(n) { padding-left: 0; border-left: 0; }
    .tiles > div:nth-child(even) { padding-left: var(--space-4); border-left: 1px solid var(--line); }
    .tiles > div:nth-child(n + 3) { border-top: 1px solid var(--line); }
    .filters .field { min-width: 0; flex: 1 1 140px; }
    /* events: one compact card each */
    .events thead { display: none; }
    .events, .events tbody, .events tr { display: block; width: 100%; }
    .events tr { display: grid; grid-template-columns: auto 1fr; gap: 2px var(--space-3); padding: var(--space-3) var(--space-4); border-top: 1px solid var(--line); }
    .events td { display: block; padding: 0; border: 0; }
    .events td[data-label='When'] { grid-column: 1 / -1; font-size: var(--step--1); color: var(--ink-3); }
    .events td[data-label='Details'] { grid-column: 1 / -1; font-size: var(--step--1); }
  }
</style>
