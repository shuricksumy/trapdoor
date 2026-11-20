<script>
  // Uptime per client, like a status page: a bar per hour or day, the share of
  // the time online, when it was last seen. A client opens a month calendar
  // with its hours online per day and the sessions of a day.
  import { api } from './api.js';
  import { currentServer } from './state.svelte.js';
  import { clientName, loadClientNames } from './names.svelte.js';
  import { duration, relative, dateTime } from './format.js';
  import { bucketSpan, level, monthGrid, sortClients, uptimePct } from './uptime.js';

  const isWG = ['wireguard', 'amneziawg'].includes(currentServer()?.type);
  const ranges = [['24h', '24 hours'], ['7d', '7 days'], ['30d', '30 days']];
  let range = $state('24h');
  let report = $state(null);
  let known = $state([]); // every current client, also the ones never seen
  let error = $state('');
  let now = $state(new Date());

  async function load() {
    try {
      now = new Date();
      const [r, list] = await Promise.all([
        api(`/history/uptime?range=${range}`),
        isWG ? api('/peers').catch(() => []) : api('/certs').catch(() => []),
      ]);
      report = r;
      known = isWG ? list.filter((p) => !p.disabled).map((p) => p.name) : list.filter((c) => c.current && c.kind !== 'server' && c.status !== 'revoked').map((c) => c.name);
      error = '';
    } catch (e) {
      error = e.message;
    }
  }
  $effect(() => {
    range;
    load();
  });
  $effect(() => {
    loadClientNames();
    const t = setInterval(load, 60000);
    return () => clearInterval(t);
  });

  const rows = $derived.by(() => {
    if (!report) return [];
    const by = new Map(report.clients.map((c) => [c.name, c]));
    for (const n of known) if (!by.has(n)) by.set(n, { name: n, online: false, last_seen: null, sessions: 0, online_seconds: 0, buckets: report.starts.map(() => 0) });
    return sortClients([...by.values()], clientName);
  });
  const spans = $derived(report ? report.starts.map((s, i) => bucketSpan(s, report.step, now, report.starts[i + 1] ?? report.to)) : []);
  const fmtStart = (s) =>
    report.step === 'day'
      ? new Date(s).toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short' })
      : new Date(s).toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' });
  const tip = (c, i) => `${fmtStart(report.starts[i])}: ${spans[i] <= 0 ? 'not yet' : c.buckets[i] ? `online ${duration(c.buckets[i])}` : 'offline'}`;

  // The calendar of one client
  let picked = $state(null); // client name
  let month = $state(null); // { y, m }
  let cal = $state(null); // uptime report of the month
  let day = $state(null); // { date: 'YYYY-MM-DD', sessions }
  function open(name) {
    picked = name;
    const d = new Date();
    month = { y: d.getFullYear(), m: d.getMonth() + 1 };
    day = null;
  }
  function shift(k) {
    const d = new Date(month.y, month.m - 1 + k, 1);
    month = { y: d.getFullYear(), m: d.getMonth() + 1 };
    day = null;
  }
  const mm = (n) => String(n).padStart(2, '0');
  $effect(() => {
    if (!picked || !month) return;
    const key = `${month.y}-${mm(month.m)}`;
    api(`/history/uptime?month=${key}`)
      .then((r) => (cal = r))
      .catch((e) => (error = e.message));
  });
  const mine = $derived(cal?.clients.find((c) => c.name === picked) ?? null);
  const weeks = $derived(month ? monthGrid(month.y, month.m) : []);
  const isFuture = (d) => new Date(month.y, month.m - 1, d) > now;
  // relative to the client's busiest day of the month, so short-use devices show their pattern too
  const busiest = $derived(Math.max(1, ...(mine?.buckets ?? [0])));
  const shade = (sec) => (sec <= 0 ? 0 : Math.max(0.15, Math.min(1, sec / busiest)));
  async function openDay(d) {
    const date = `${month.y}-${mm(month.m)}-${mm(d)}`;
    day = { date, sessions: null };
    try {
      const list = await api(`/history/sessions?from=${date}&to=${date}&name=${encodeURIComponent(picked)}&limit=200`);
      day = { date, sessions: list.slice().reverse() };
    } catch (e) {
      day = { date, sessions: [], error: e.message };
    }
  }
</script>

{#if error}<div class="banner bad" role="alert">{error}</div>{/if}

{#if picked && month}
  <section class="panel cal">
    <div class="panel-head">
      <h2>{clientName(picked)}</h2>
      <div class="nav">
        <button class="btn small ghost" onclick={() => shift(-1)} aria-label="Previous month">←</button>
        <span class="month">{new Date(month.y, month.m - 1, 1).toLocaleDateString([], { month: 'long', year: 'numeric' })}</span>
        <button class="btn small ghost" onclick={() => shift(1)} aria-label="Next month" disabled={new Date(month.y, month.m, 1) > now}>→</button>
        <button class="btn small" onclick={() => (picked = null)}>Close</button>
      </div>
    </div>
    <div class="panel-body">
      <div class="grid" role="grid" aria-label="Hours online per day">
        {#each ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as w (w)}<span class="wd">{w}</span>{/each}
        {#each weeks as week, wi (wi)}
          {#each week as cell, ci (ci)}
            {#if cell}
              {@const sec = mine?.buckets[cell.i] ?? 0}
              <button class="day" class:future={isFuture(cell.day)} class:sel={day?.date.endsWith(`-${mm(cell.day)}`)} disabled={isFuture(cell.day)}
                style="--a: {shade(sec)}" onclick={() => openDay(cell.day)} title={sec ? `online ${duration(sec)}` : 'offline'}>
                <span class="n">{cell.day}</span>
                <span class="h">{sec ? duration(sec) : ''}</span>
              </button>
            {:else}
              <span></span>
            {/if}
          {/each}
        {/each}
      </div>
      <p class="hint">This month: online {duration(mine?.online_seconds ?? 0)} in {mine?.sessions ?? 0} sessions. Pick a day for its sessions.</p>
      {#if day}
        <h3>{new Date(day.date + 'T12:00:00').toLocaleDateString([], { weekday: 'long', day: 'numeric', month: 'long' })}</h3>
        {#if !day.sessions}<p class="muted">Loading…</p>
        {:else if !day.sessions.length}<p class="muted">Not connected that day.</p>
        {:else}
          <ul class="sessions">
            {#each day.sessions as s, i (i)}
              <li>
                <span class="mono">{dateTime(s.connected_at)}</span> – <span class="mono">{s.disconnected_at ? dateTime(s.disconnected_at) : 'now'}</span>
                <span class="faint">{duration(s.duration_seconds)} · from {s.real_address || '–'}</span>
              </li>
            {/each}
          </ul>
        {/if}
      {/if}
    </div>
  </section>
{/if}

<section class="panel">
  <div class="panel-head">
    <h2>Uptime</h2>
    <div class="seg" role="radiogroup" aria-label="Period">
      {#each ranges as [k, label] (k)}<button role="radio" aria-checked={range === k} onclick={() => (range = k)}>{label}</button>{/each}
    </div>
  </div>
  {#if !report}
    <p class="panel-body muted">Loading…</p>
  {:else if rows.length === 0}
    <p class="panel-body muted">No clients yet.</p>
  {:else}
    <ul class="rows">
      {#each rows as c (c.name)}
        <li>
          <button class="who" onclick={() => open(c.name)} title="Calendar of {clientName(c.name)}">
            <i class="dot" class:on={c.online}></i>
            <span class="name">{clientName(c.name)}</span>
          </button>
          <div class="bars" role="img" aria-label="{clientName(c.name)}: online {uptimePct(c.online_seconds, report, now)}% of the period">
            {#each c.buckets as sec, i (i)}<i class="b {level(sec, spans[i])}" title={tip(c, i)}></i>{/each}
          </div>
          <span class="pct">{uptimePct(c.online_seconds, report, now)}%</span>
          <span class="seen faint small">{c.online ? 'online now' : c.last_seen ? `seen ${relative(c.last_seen)}` : 'never seen'}</span>
        </li>
      {/each}
    </ul>
    <p class="panel-body hint legend">
      <i class="b full"></i> online <i class="b part"></i> part of the {report.step} <i class="b none"></i> offline · from the session history; WireGuard counts a client as gone 3 minutes after its last handshake.
    </p>
  {/if}
</section>

<style>
  .panel { margin-bottom: var(--space-5); }
  .panel-head { display: flex; justify-content: space-between; align-items: center; gap: var(--space-3); flex-wrap: wrap; }
  .seg { display: inline-flex; gap: 2px; }
  .seg button { height: 30px; padding: 0 var(--space-3); border: 0; border-radius: var(--radius-s); background: transparent; color: var(--ink-2); font: inherit; cursor: pointer; }
  .seg button[aria-checked='true'] { background: var(--surface); color: var(--ink); box-shadow: inset 0 0 0 1px var(--line); }
  .rows { list-style: none; margin: 0; padding: 0 var(--space-5) var(--space-3); }
  .rows li { display: grid; grid-template-columns: 200px minmax(0, 1fr) 64px 120px; gap: var(--space-3); align-items: center; padding: 10px 0; border-top: 1px solid var(--line); }
  .rows li:first-child { border-top: 0; }
  .who { display: flex; align-items: center; gap: 8px; min-width: 0; padding: 0; border: 0; background: none; color: var(--ink); font: inherit; font-weight: 600; cursor: pointer; text-align: left; }
  .who:hover .name { text-decoration: underline; }
  .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ink-3); opacity: 0.6; flex: none; }
  .dot.on { background: var(--ok); opacity: 1; }
  .bars { display: flex; gap: 2px; height: 26px; }
  .b { flex: 1; min-width: 2px; border-radius: 3px; }
  .b.full { background: var(--ok); }
  .b.part { background: color-mix(in srgb, var(--ok) 45%, var(--sunken)); }
  .b.none { background: var(--sunken); box-shadow: inset 0 0 0 1px var(--line); }
  .b.future { background: transparent; box-shadow: inset 0 0 0 1px var(--line); opacity: 0.4; }
  .pct { text-align: right; font-variant-numeric: tabular-nums; font-weight: 600; }
  .seen { text-align: right; }
  .legend { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
  .legend .b { display: inline-block; flex: none; width: 10px; height: 12px; }
  .cal .nav { display: flex; align-items: center; gap: var(--space-2); }
  .month { min-width: 140px; text-align: center; font-weight: 600; }
  .grid { display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); gap: 6px; max-width: 640px; }
  .wd { font-size: var(--step--1); color: var(--ink-3); text-align: center; }
  .day { display: grid; gap: 2px; padding: 6px; min-height: 52px; border: 1px solid var(--line); border-radius: var(--radius-s); color: var(--ink); font: inherit; text-align: left; cursor: pointer;
    background: color-mix(in srgb, var(--ok) calc(var(--a) * 70%), var(--sunken)); }
  .day.future { opacity: 0.35; cursor: default; }
  .day.sel { box-shadow: 0 0 0 2px var(--accent); }
  .day .n { font-weight: 600; font-size: var(--step--1); }
  .day .h { font-size: 11px; }
  h3 { margin: var(--space-4) 0 var(--space-2); font-size: var(--step-0); }
  .sessions { list-style: none; margin: 0; padding: 0; display: grid; gap: 4px; }
  .sessions li { display: flex; gap: var(--space-2); flex-wrap: wrap; }
  @media (max-width: 760px) {
    .rows li { grid-template-columns: minmax(0, 1fr) auto; }
    .bars { grid-column: 1 / -1; order: 3; }
    .seen { display: none; }
  }
</style>
