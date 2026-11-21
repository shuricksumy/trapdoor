<script>
  // Speed and usage limits per client: the server's defaults, each client's
  // own values, usage in the period, reset. The web part checks usage every
  // minute; the agent applies the speeds with tc.
  import { api } from '../lib/api.js';
  import { currentServer, toast } from '../lib/state.svelte.js';
  import { clientName, clientTitle, loadClientNames } from '../lib/names.svelte.js';
  import { bytes } from '../lib/format.js';
  import Dialog from '../lib/Dialog.svelte';
  import { speedPair, quotaText, usagePct, periodText, ownGroups, toForm, fromForm, overText, summary, resetsIn } from '../lib/limits.js';

  const server = currentServer();
  let data = $state(null);
  let error = $state('');
  let find = $state('');
  let busy = $state('');
  let now = $state(Date.now());

  async function load() {
    try {
      data = await api('/limits');
      error = '';
    } catch (e) {
      error = e.message;
    }
    now = Date.now();
  }
  $effect(() => {
    loadClientNames();
    load();
    const t = setInterval(load, 60_000);
    return () => clearInterval(t);
  });

  const rows = $derived(
    (data?.clients ?? []).filter((r) => !find || `${r.name} ${clientName(r.name)}`.toLowerCase().includes(find.toLowerCase())),
  );
  const tiles = $derived(summary(data?.clients ?? []));
  const def = $derived(data?.default);
  const sh = $derived(data?.shaping);
  const addr = (name) => sh?.addresses?.[name];

  // The dialog: the server's defaults (name = ''), or one client's own values.
  let dlg = $state({ open: false, name: '', form: null, own: null, error: '', busy: false });
  function edit(row) {
    if (!row) {
      dlg = { open: true, name: '', form: toForm(def), own: { speed: true, usage: true, over: true }, error: '', busy: false };
      return;
    }
    dlg = { open: true, name: row.name, form: toForm(row.limit), own: ownGroups(row.own), error: '', busy: false };
  }
  async function save(e) {
    e?.preventDefault();
    let body;
    try {
      body = fromForm(dlg.form, dlg.own);
    } catch (err) {
      dlg.error = err.message;
      return;
    }
    dlg.busy = true;
    try {
      data = dlg.name
        ? await api(`/limits/clients/${encodeURIComponent(dlg.name)}`, { method: 'PUT', body })
        : await api('/limits/default', { method: 'PUT', body });
      dlg.open = false;
      toast(dlg.name ? `Limits of ${clientName(dlg.name)} saved` : 'Defaults saved');
    } catch (err) {
      dlg.error = err.message;
    } finally {
      dlg.busy = false;
    }
  }
  async function reset(row) {
    busy = row.name;
    try {
      data = await api(`/limits/clients/${encodeURIComponent(row.name)}/reset`, { method: 'POST' });
      toast(`${clientName(row.name)} starts from zero`);
    } catch (err) {
      toast(err.message, 'bad');
    } finally {
      busy = '';
    }
  }

  const stateText = { full: 'full speed', capped: 'capped', slowed: 'slowed', blocked: 'blocked' };
  const stateKind = { full: 'off', capped: 'ok', slowed: 'warn', blocked: 'bad' };
  const nowText = (r) => (r.state === 'blocked' ? 'blocked' : r.state === 'full' ? 'full speed' : speedPair(r.speed.down ?? 0, r.speed.up ?? 0));
  const defaultOf = (g) =>
    g === 'speed' ? speedPair(def.down, def.up) : g === 'usage' ? (def.quota_mb ? `${quotaText(def.quota_mb)} ${periodText[def.period]}` : 'no limit') : overText(def);
</script>

<header class="head">
  <div>
    <h1>Limits</h1>
    <p class="muted">Speed and usage limits for each client of <strong>{server?.name ?? 'this server'}</strong>. A client over its usage limit is slowed down or blocked until the period ends.</p>
  </div>
</header>

{#if error}<div class="banner bad" role="alert">{error}</div>{/if}

{#if data}
  {#if data.shaping_error}
    <div class="banner warn">Speeds are not applied: {data.shaping_error}</div>
  {:else if sh?.error}
    <div class="banner bad">Speeds are not applied: {sh.error}</div>
  {:else if sh?.waiting}
    <div class="banner warn">The server is not running; its speeds are applied when it starts.</div>
  {/if}

  <section class="tiles" aria-label="Summary">
    <div><span class="k">Slowed or blocked now</span><span class="v" class:bad={tiles.slowed > 0}>{tiles.slowed}</span></div>
    <div><span class="k">Over 80% of the limit</span><span class="v" class:warn={tiles.near > 0}>{tiles.near}</span></div>
    <div><span class="k">With a usage limit</span><span class="v">{tiles.withLimit}<small> / {data.clients.length}</small></span></div>
    <div><span class="k">Rules</span><span class="v small-v">{#if sh?.applied}<span class="pill ok">applied</span>{:else if sh?.waiting}<span class="pill warn">waiting</span>{:else}<span class="pill bad">not applied</span>{/if}</span></div>
  </section>

  <section class="panel">
    <div class="panel-head">
      <h2>Defaults for every client</h2>
      <button class="btn small" onclick={() => edit(null)}>Edit</button>
    </div>
    <dl class="panel-body defaults">
      <div><dt>Speed</dt><dd>{speedPair(def.down, def.up)}</dd></div>
      <div><dt>Usage limit</dt><dd>{def.quota_mb ? `${quotaText(def.quota_mb)} ${periodText[def.period]}` : 'no limit'}</dd></div>
      <div><dt>When reached</dt><dd>{overText(def)}</dd></div>
    </dl>
  </section>

  <section class="panel">
    <div class="panel-head">
      <h2>Clients</h2>
      <input class="search" type="search" placeholder="Find a client" bind:value={find} aria-label="Find a client" />
    </div>
    {#if !data.clients.length}
      <p class="panel-body muted">No clients yet.</p>
    {:else}
      <div class="table-wrap">
        <table class="cards">
          <thead><tr><th>Client</th><th>Speed now</th><th class="use">Usage this period</th><th>Resets</th><th>Own settings</th><th><span class="sr">Actions</span></th></tr></thead>
          <tbody>
            {#each rows as r (r.name)}
              {@const pct = usagePct(r.used, r.limit.quota_mb)}
              {@const og = ownGroups(r.own)}
              <tr>
                <td data-label="Client"><strong title={clientTitle(r.name)}>{clientName(r.name)}</strong></td>
                <td data-label="Speed now">
                  <span class="pill {stateKind[r.state]}" title={stateText[r.state]}>{nowText(r)}</span>
                  {#if r.state !== 'full' && !addr(r.name)}<span class="faint small block">applies while connected</span>{/if}
                </td>
                <td data-label="Usage" class="use">
                  {#if pct != null}
                    <div class="bar" role="meter" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.min(pct, 100)} aria-label="Usage">
                      <i style="width: {Math.min(pct, 100)}%" class:warn={pct >= 80 && pct < 100} class:bad={pct >= 100}></i>
                    </div>
                    <span class="small">{bytes(r.used)} <span class="faint">of {quotaText(r.limit.quota_mb)} {periodText[r.limit.period]} · {pct}%</span></span>
                  {:else}
                    <span class="small">{bytes(r.used)} <span class="faint">this month, no limit</span></span>
                  {/if}
                </td>
                <td data-label="Resets" class="small">{#if r.limit.quota_mb}{resetsIn(r.period_end, now)}{#if r.reset_at}<span class="faint block">reset {new Date(r.reset_at).toLocaleDateString([], { month: 'short', day: 'numeric' })}</span>{/if}{:else}<span class="faint">–</span>{/if}</td>
                <td data-label="Own settings">
                  {#if og.speed || og.usage || og.over}
                    <div class="tags">
                      {#if og.speed}<span class="tag">{speedPair(r.limit.down, r.limit.up)}</span>{/if}
                      {#if og.usage}<span class="tag">{r.limit.quota_mb ? `${quotaText(r.limit.quota_mb)} ${periodText[r.limit.period]}` : 'no usage limit'}</span>{/if}
                      {#if og.over}<span class="tag">{r.limit.block ? 'blocks' : `slows to ${speedPair(r.limit.slow_down, r.limit.slow_up)}`}</span>{/if}
                    </div>
                  {:else}<span class="faint">defaults</span>{/if}
                </td>
                <td class="actions">
                  {#if r.limit.quota_mb && r.used > 0}<button class="btn small ghost" disabled={busy === r.name} onclick={() => reset(r)} title="Count from zero for the rest of the period">Reset</button>{/if}
                  <button class="btn small ghost" onclick={() => edit(r)}>Edit</button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <p class="panel-body hint">Speeds are per client. Usage counts both directions and is checked every minute; a new client and an OpenVPN client with an automatic address get their speed within a minute of connecting.</p>
    {/if}
  </section>
{/if}

<Dialog bind:open={dlg.open} title={dlg.name ? `Limits of ${clientName(dlg.name)}` : 'Defaults for every client'}>
  {#if dlg.form}
    <form class="dlg" onsubmit={save} id="limits-form">
      <!-- In the legend: a disabled fieldset keeps its legend's controls usable. -->
      {#snippet group(key, title)}
        <legend>
          {#if dlg.name}
            <label class="check own">
              <input type="checkbox" bind:checked={dlg.own[key]} />
              <span><strong>{title}</strong> <span class="faint">{dlg.own[key] ? 'its own' : `the default: ${defaultOf(key)}`}</span></span>
            </label>
          {:else}
            <strong class="gtitle">{title}</strong>
          {/if}
        </legend>
      {/snippet}

      <fieldset disabled={!!dlg.name && !dlg.own.speed}>
        {@render group('speed', 'Speed')}
        <div class="row">
          <label class="field"><span>Download, Mbit/s</span><input bind:value={dlg.form.down} inputmode="decimal" placeholder="unlimited" /></label>
          <label class="field"><span>Upload, Mbit/s</span><input bind:value={dlg.form.up} inputmode="decimal" placeholder="unlimited" /></label>
        </div>
      </fieldset>

      <fieldset disabled={!!dlg.name && !dlg.own.usage}>
        {@render group('usage', 'Usage limit')}
        <div class="row">
          <label class="field"><span>GB (both directions)</span><input bind:value={dlg.form.quota} inputmode="decimal" placeholder="no limit" /></label>
          <label class="field"><span>Per</span>
            <select bind:value={dlg.form.period}><option value="day">day</option><option value="week">week (from Monday)</option><option value="month">month</option></select>
          </label>
        </div>
      </fieldset>

      <fieldset disabled={!!dlg.name && !dlg.own.over}>
        {@render group('over', 'When the limit is reached')}
        <label class="check"><input type="radio" name="over" value={false} bind:group={dlg.form.block} /><span>Slow down to</span></label>
        <div class="row indent">
          <label class="field"><span>Download, Mbit/s</span><input bind:value={dlg.form.slow_down} inputmode="decimal" disabled={dlg.form.block} /></label>
          <label class="field"><span>Upload, Mbit/s</span><input bind:value={dlg.form.slow_up} inputmode="decimal" disabled={dlg.form.block} /></label>
        </div>
        <label class="check"><input type="radio" name="over" value={true} bind:group={dlg.form.block} /><span>Block until the period ends <span class="faint">(stays connected, nothing gets through)</span></span></label>
      </fieldset>
      {#if dlg.error}<p class="err" role="alert">{dlg.error}</p>{/if}
    </form>
  {/if}
  {#snippet actions()}
    <button class="btn" onclick={() => (dlg.open = false)}>Cancel</button>
    <button class="btn primary" type="submit" form="limits-form" disabled={dlg.busy}>{dlg.busy ? 'Saving…' : 'Save'}</button>
  {/snippet}
</Dialog>

<style>
  .head { margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); max-width: 72ch; }
  .banner { margin-bottom: var(--space-4); }
  .panel { margin-bottom: var(--space-5); }
  .panel-head { display: flex; justify-content: space-between; align-items: center; gap: var(--space-3); flex-wrap: wrap; }
  .tiles { display: grid; grid-template-columns: repeat(4, 1fr); margin-bottom: var(--space-5); border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); }
  .tiles > div { display: grid; gap: 2px; padding: var(--space-4) var(--space-4) var(--space-4) 0; align-content: start; }
  .tiles > div + div { padding-left: var(--space-4); border-left: 1px solid var(--line); }
  .k { font-size: var(--step--1); color: var(--ink-2); }
  .v { font-size: var(--step-2); font-weight: 500; letter-spacing: -0.01em; }
  .v small { font-size: var(--step-0); color: var(--ink-3); }
  .v.bad { color: var(--bad); }
  .v.warn { color: var(--warn); }
  .small-v { font-size: var(--step-0); padding-top: 6px; }
  .defaults { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--space-4); margin: 0; }
  .defaults dt { font-size: var(--step--1); color: var(--ink-3); }
  .defaults dd { margin: 2px 0 0; font-weight: 500; }
  .search { max-width: 240px; height: 32px; }
  .use { min-width: 220px; }
  .bar { height: 6px; margin-bottom: 4px; border-radius: 3px; background: var(--sunken); overflow: hidden; max-width: 240px; }
  .bar i { display: block; height: 100%; border-radius: 3px; background: var(--ok); }
  .bar i.warn { background: var(--warn); }
  .bar i.bad { background: var(--bad); }
  .small { font-size: var(--step--1); }
  .block { display: block; }
  .tags { display: flex; flex-wrap: wrap; gap: 4px; }
  .tag { padding: 2px 7px; border-radius: 6px; background: var(--accent-soft); font-size: 12px; white-space: nowrap; }
  .actions { text-align: right; white-space: nowrap; }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
  .dlg { display: grid; gap: var(--space-4); }
  fieldset { display: grid; gap: var(--space-2); margin: 0; padding: 0 0 var(--space-3); border: 0; border-bottom: 1px solid var(--line); min-width: 0; }
  fieldset:last-of-type { border-bottom: 0; }
  fieldset:disabled .row, fieldset:disabled > .check:not(.own) { opacity: 0.45; }
  .own { align-items: flex-start; }
  .own input, .check input { height: auto; }
  .gtitle { display: block; }
  legend { padding: 0; margin-bottom: var(--space-1); }
  input:disabled { opacity: 0.55; }
  .row { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); }
  .indent { padding-left: 24px; }
  .err { margin: 0; color: var(--bad); }
  @media (max-width: 760px) {
    .tiles { grid-template-columns: 1fr 1fr; }
    .tiles > div:nth-child(3) { padding-left: 0; border-left: 0; }
    .tiles > div:nth-child(n + 3) { border-top: 1px solid var(--line); }
    .defaults { grid-template-columns: 1fr; gap: var(--space-2); }
  }
  @media (max-width: 640px) {
    .search { max-width: none; width: 100%; }
    .cards thead { display: none; }
    .cards, .cards tbody, .cards tr, .cards td { display: block; width: 100%; }
    .cards tr { padding: var(--space-3) var(--space-4); border-top: 1px solid var(--line); }
    .cards td { padding: 3px 0; border: 0; }
    .cards td[data-label]:not(:first-child) { display: grid; grid-template-columns: 104px minmax(0, 1fr); column-gap: var(--space-2); align-items: start; }
    .cards td[data-label]:not(:first-child)::before { content: attr(data-label); color: var(--ink-3); font-size: var(--step--1); padding-top: 2px; }
    .cards td[data-label]:not(:first-child) > * { grid-column: 2; justify-self: start; }
    .cards .actions { text-align: left; padding-top: var(--space-2); }
    .use { min-width: 0; }
    .cards td[data-label]:not(:first-child) > .bar { justify-self: stretch; width: 100%; }
    .indent { padding-left: 0; }
  }
</style>
