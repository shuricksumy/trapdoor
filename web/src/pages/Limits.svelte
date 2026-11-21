<script>
  // Speed and usage limits per client: the server's defaults, each client's
  // own values, usage in the period, reset. The web part checks usage every
  // minute; the agent applies the speeds with tc.
  import { api } from '../lib/api.js';
  import { currentServer, toast } from '../lib/state.svelte.js';
  import { clientName, clientTitle, loadClientNames } from '../lib/names.svelte.js';
  import { bytes } from '../lib/format.js';
  import Dialog from '../lib/Dialog.svelte';
  import { speedPair, quotaText, usagePct, periodText, ownGroups, toForm, fromForm, overText, summary, resetsIn, serverForm, fromServerForm, money, typeSpeed } from '../lib/limits.js';

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
      dlg = { open: true, name: '', form: toForm(def), own: { speed: true, usage: true, over: true, prio: true }, error: '', busy: false };
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

  // The server's own settings: link speed (priorities), alarms, prices.
  const ss = $derived(data?.server ?? {});
  const tr = $derived(data?.traffic ?? {});
  const priced = $derived(ss.price_in > 0 || ss.price_out > 0);
  const linked = $derived(ss.link_down > 0 || ss.link_up > 0);
  const cur = $derived(ss.currency || '$');
  let sdlg = $state({ open: false, form: null, error: '', busy: false });
  const editServer = () => (sdlg = { open: true, form: serverForm(ss), error: '', busy: false });
  async function saveServer(e) {
    e?.preventDefault();
    let body;
    try {
      body = fromServerForm(sdlg.form);
    } catch (err) {
      sdlg.error = err.message;
      return;
    }
    sdlg.busy = true;
    try {
      data = await api('/limits/server', { method: 'PUT', body });
      sdlg.open = false;
      toast('Server settings saved');
    } catch (err) {
      sdlg.error = err.message;
    } finally {
      sdlg.busy = false;
    }
  }
  const alarmText = $derived(
    [['alarm_hour_mb', 'an hour'], ['alarm_day_mb', 'a day'], ['alarm_month_mb', 'a month']]
      .filter(([k]) => ss[k] > 0)
      .map(([k, w]) => `${quotaText(ss[k])} ${w}`)
      .join(', '),
  );
  const prioText = { high: 'high', normal: 'normal', low: 'low' };
  const speedHint = typeSpeed[server?.type] ?? typeSpeed.openvpn;

  // compact for the list: "50 / 20 Mbit/s", "unlimited", "1 Mbit/s"
  const mb = (k) => (k ? String(Math.round(k / 10) / 100) : '∞');
  const sp = (d, u) => (d === u ? speedPair(d, u) : `${mb(d)} / ${mb(u)} Mbit/s`);
  const quota = (l) => (l.quota_mb ? `${quotaText(l.quota_mb)} / ${l.period}` : 'no limit');
  const nowShort = (r) => (r.state === 'slowed' ? `slowed · ${sp(r.speed.down ?? 0, r.speed.up ?? 0)}` : stateText[r.state]);
  const stateText = { full: 'full speed', capped: 'normal', slowed: 'slowed', blocked: 'blocked' };
  const stateKind = { full: 'off', capped: 'ok', slowed: 'warn', blocked: 'bad' };
  const defaultOf = (g) =>
    g === 'speed' ? speedPair(def.down, def.up) : g === 'usage' ? (def.quota_mb ? `${quotaText(def.quota_mb)} ${periodText[def.period]}` : 'no limit') : g === 'prio' ? def.priority : overText(def);
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
    <div><span class="k">Server today</span><span class="v">{bytes(tr.day)}{#if ss.alarm_day_mb}<small> / {quotaText(ss.alarm_day_mb)}</small>{/if}</span>{#if ss.alarm_hour_mb}<span class="faint small">this hour {bytes(tr.hour)} of {quotaText(ss.alarm_hour_mb)}</span>{/if}</div>
    {#if priced}
      <div><span class="k">Cost this month</span><span class="v">{money(tr.cost, cur)}</span>{#if tr.projected}<span class="faint small">≈ {money(tr.projected, cur)} by the month's end</span>{/if}</div>
    {:else}
      <div><span class="k">With a usage limit</span><span class="v">{tiles.withLimit}<small> / {data.clients.length}</small></span></div>
    {/if}
    <div><span class="k">Rules</span><span class="v small-v">{#if sh?.applied}<span class="pill ok">applied</span>{:else if sh?.waiting}<span class="pill warn">waiting</span>{:else}<span class="pill bad">not applied</span>{/if}</span></div>
  </section>

  <section class="srvbar" aria-label="Server settings">
    <dl>
      <div><dt>Link</dt><dd>{linked ? `${sp(ss.link_down, ss.link_up)} · priorities on` : 'not set · priorities off'}</dd></div>
      <div><dt>Alarms</dt><dd>{alarmText || 'none'}</dd></div>
      <div><dt>Price per GB</dt><dd>{priced ? `${money(ss.price_in, cur)} in · ${money(ss.price_out, cur)} out` : 'not set'}</dd></div>
    </dl>
    <button class="btn small" onclick={editServer}>Server settings</button>
  </section>

  <section class="panel">
    <div class="panel-head">
      <h2>Clients</h2>
      <input class="search" type="search" placeholder="Find a client" bind:value={find} aria-label="Find a client" />
    </div>
    <div class="table-wrap">
      <table class="list">
        <thead><tr><th>Client</th><th>Speed ↓ / ↑</th><th>Priority</th><th>Usage limit</th><th class="use">Used</th><th>Then</th><th>Now</th>{#if priced}<th class="num">Month</th>{/if}<th><span class="sr">Actions</span></th></tr></thead>
        <tbody>
          <tr class="def" onclick={() => edit(null)}>
            <td class="who"><strong>Default</strong><span class="faint small block">every client</span></td>
            <td class="c-speed">{sp(def.down, def.up)}</td>
            <td class="c-prio"><span class="prio {def.priority}">{prioText[def.priority]}</span></td>
            <td class="c-quota">{quota(def)}</td>
            <td class="use"><span class="faint small">{bytes((data.clients ?? []).reduce((n, r) => n + r.used, 0))} in all</span></td>
            <td class="c-over">{def.quota_mb ? (def.block ? 'block' : `slow to ${sp(def.slow_down, def.slow_up)}`) : '–'}</td>
            <td class="c-now"></td>
            {#if priced}<td class="num c-cost">{money(tr.cost, cur)}</td>{/if}
            <td class="actions"><button class="btn small ghost" onclick={(e) => (e.stopPropagation(), edit(null))}>Edit</button></td>
          </tr>
          {#each rows as r (r.name)}
            {@const pct = usagePct(r.used, r.limit.quota_mb)}
            {@const og = ownGroups(r.own)}
            <tr onclick={() => edit(r)}>
              <td class="who">
                <strong title={clientTitle(r.name)}>{clientName(r.name)}</strong>
                <span class="pill {stateKind[r.state]} m-only">{nowShort(r)}</span>
              </td>
              <td class="c-speed" class:inh={!og.speed}>{sp(r.limit.down, r.limit.up)}</td>
              <td class="c-prio" class:inh={!og.prio}><span class="prio {r.limit.priority}">{prioText[r.limit.priority]}</span></td>
              <td class="c-quota" class:inh={!og.usage}>{quota(r.limit)}{#if r.limit.quota_mb}<span class="faint small block">resets {resetsIn(r.period_end, now)}</span>{/if}</td>
              <td class="use">
                {#if pct != null}
                  <div class="bar" role="meter" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.min(pct, 100)} aria-label="Usage">
                    <i style="width: {Math.min(pct, 100)}%" class:warn={pct >= 80 && pct < 100} class:bad={pct >= 100}></i>
                  </div>
                  <span class="small">{bytes(r.used)} <span class="faint">· {pct}%</span></span>
                {:else}
                  <span class="small">{bytes(r.used)}</span>
                {/if}
              </td>
              <td class="c-over" class:inh={!og.over}>{r.limit.quota_mb ? (r.limit.block ? 'block' : `slow to ${sp(r.limit.slow_down, r.limit.slow_up)}`) : '–'}</td>
              <td class="c-now"><span class="pill {stateKind[r.state]}">{nowShort(r)}</span>{#if r.state !== 'full' && !addr(r.name)}<span class="faint small block">while connected</span>{/if}</td>
              {#if priced}<td class="num c-cost small" title="{bytes(r.month.out)} out, {bytes(r.month.in)} in this month">{money(r.cost, cur)}</td>{/if}
              <td class="actions">
                {#if r.limit.quota_mb && r.used > 0}<button class="btn small ghost" disabled={busy === r.name} onclick={(e) => (e.stopPropagation(), reset(r))} title="Count from zero for the rest of the period">Reset</button>{/if}
                <button class="btn small ghost" onclick={(e) => (e.stopPropagation(), edit(r))}>Edit</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="panel-body hint"><span class="inh">Grey</span>: from the default · <strong>black</strong>: the client's own. Speeds are per client; usage counts both directions and is checked every minute.{#if !linked} Priorities work once the server's link speed is set (Server settings).{/if}</p>
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
        <p class="info">ⓘ {speedHint} The server's own network and its plan set a limit too; a speed above what the server can do changes nothing.</p>
      </fieldset>

      <fieldset disabled={!!dlg.name && !dlg.own.prio}>
        {@render group('prio', 'Priority')}
        <select bind:value={dlg.form.priority} aria-label="Priority">
          <option value="high">High: goes first when the link is busy</option>
          <option value="normal">Normal</option>
          <option value="low">Low: gets what is left</option>
        </select>
        {#if !linked}<p class="info warn">Priorities need the server's link speed: set it in Server settings.</p>{/if}
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

<Dialog bind:open={sdlg.open} title="Server settings">
  {#if sdlg.form}
    <form class="dlg" onsubmit={saveServer} id="server-form">
      <fieldset>
        <legend><strong class="gtitle">Link speed, for priorities</strong></legend>
        <div class="row">
          <label class="field"><span>To clients, Mbit/s</span><input bind:value={sdlg.form.link_down} inputmode="decimal" placeholder="not set" /></label>
          <label class="field"><span>From clients, Mbit/s</span><input bind:value={sdlg.form.link_up} inputmode="decimal" placeholder="not set" /></label>
        </div>
        <p class="info">ⓘ Set it a little below what really limits this server (its uplink, the cloud plan, or what the VPN can do), e.g. 90 for a 100 Mbit/s line. Traffic is then kept just under it, so a queue forms here and high-priority clients go first. {speedHint}</p>
      </fieldset>
      <fieldset>
        <legend><strong class="gtitle">Traffic alarms</strong> <span class="faint">a message at 80% and 100%, nothing is slowed</span></legend>
        <div class="row three">
          <label class="field"><span>GB per hour</span><input bind:value={sdlg.form.alarm_hour} inputmode="decimal" placeholder="none" /></label>
          <label class="field"><span>GB per day</span><input bind:value={sdlg.form.alarm_day} inputmode="decimal" placeholder="none" /></label>
          <label class="field"><span>GB per month</span><input bind:value={sdlg.form.alarm_month} inputmode="decimal" placeholder="none" /></label>
        </div>
      </fieldset>
      <fieldset>
        <legend><strong class="gtitle">Price of traffic</strong> <span class="faint">for information</span></legend>
        <div class="row three">
          <label class="field"><span>Per GB in</span><input bind:value={sdlg.form.price_in} inputmode="decimal" placeholder="0" /></label>
          <label class="field"><span>Per GB out</span><input bind:value={sdlg.form.price_out} inputmode="decimal" placeholder="0" /></label>
          <label class="field"><span>Currency</span><input bind:value={sdlg.form.currency} maxlength="4" /></label>
        </div>
        <p class="info">ⓘ In: what clients send (their upload), out: what they get (their download). On a cloud server both usually cost: a download leaves the server to the client, an upload leaves it to the internet. E.g. AWS: about 0.09 per GB each way after the free allowance.</p>
      </fieldset>
      {#if sdlg.error}<p class="err" role="alert">{sdlg.error}</p>{/if}
    </form>
  {/if}
  {#snippet actions()}
    <button class="btn" onclick={() => (sdlg.open = false)}>Cancel</button>
    <button class="btn primary" type="submit" form="server-form" disabled={sdlg.busy}>{sdlg.busy ? 'Saving…' : 'Save'}</button>
  {/snippet}
</Dialog>

<style>
  .head { margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); max-width: 72ch; }
  .banner { margin-bottom: var(--space-4); }
  .panel { margin-bottom: var(--space-5); }
  .panel-head { display: flex; justify-content: space-between; align-items: center; gap: var(--space-3); flex-wrap: wrap; }
  .tiles { display: grid; grid-template-columns: repeat(5, 1fr); margin-bottom: var(--space-5); border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); }
  .tiles > div { display: grid; gap: 2px; padding: var(--space-4) var(--space-4) var(--space-4) 0; align-content: start; }
  .tiles > div + div { padding-left: var(--space-4); border-left: 1px solid var(--line); }
  .k { font-size: var(--step--1); color: var(--ink-2); }
  .v { font-size: var(--step-2); font-weight: 500; letter-spacing: -0.01em; }
  .v small { font-size: var(--step-0); color: var(--ink-3); }
  .v.bad { color: var(--bad); }
  .v.warn { color: var(--warn); }
  .small-v { font-size: var(--step-0); padding-top: 6px; }
  .search { max-width: 240px; height: 32px; }
  .use { min-width: 220px; }
  .bar { height: 6px; margin-bottom: 4px; border-radius: 3px; background: var(--sunken); overflow: hidden; max-width: 240px; }
  .bar i { display: block; height: 100%; border-radius: 3px; background: var(--ok); }
  .bar i.warn { background: var(--warn); }
  .bar i.bad { background: var(--bad); }
  .small { font-size: var(--step--1); }
  .block { display: block; }
  .actions { text-align: right; white-space: nowrap; }
  .list tbody tr { cursor: pointer; }
  .list tbody tr:hover td { background: var(--sunken); }
  .list tr.def td { background: color-mix(in srgb, var(--accent-soft) 55%, transparent); border-bottom: 2px solid var(--line); }
  .list td { white-space: nowrap; }
  .list th, .list td { padding-left: 8px; padding-right: 8px; }
  .list th { white-space: nowrap; }
  .list .actions .btn { padding: 0 var(--space-2); }
  .list td.use { white-space: normal; width: 130px; min-width: 120px; }
  .list .bar { max-width: 120px; }
  .inh { color: var(--ink-3); }
  .prio { display: inline-block; padding: 1px 8px; border-radius: 6px; font-size: 12px; font-weight: 500; background: var(--sunken); color: var(--ink-2); }
  .prio.high { background: var(--accent-soft); color: var(--ink); }
  .prio.low { background: none; border: 1px dashed var(--line); color: var(--ink-3); }
  .inh .prio { opacity: 0.7; }
  .srvbar { display: flex; align-items: center; justify-content: space-between; gap: var(--space-4); flex-wrap: wrap; margin-bottom: var(--space-5); padding: var(--space-3) var(--space-4); border: 1px solid var(--line); border-radius: var(--radius-m); background: var(--surface); }
  .srvbar dl { display: flex; flex-wrap: wrap; gap: var(--space-2) var(--space-6, 32px); margin: 0; }
  .srvbar dt { font-size: var(--step--1); color: var(--ink-3); }
  .srvbar dd { margin: 0; font-weight: 500; }
  .info { margin: 0; padding: var(--space-2) var(--space-3); border-radius: var(--radius-s); background: var(--sunken); color: var(--ink-2); font-size: var(--step--1); line-height: 1.45; }
  .info.warn { background: var(--warn-soft); color: var(--warn); }
  .row.three { grid-template-columns: 1fr 1fr 1fr; }
  td.num, th.num { text-align: right; }
  .m-only { display: none; }
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
    .tiles > div:nth-child(odd) { padding-left: 0; border-left: 0; }
    .tiles > div:nth-child(5) { grid-column: 1 / -1; }
    .tiles > div:nth-child(n + 3) { border-top: 1px solid var(--line); }
  }
  @media (max-width: 640px) {
    .search { max-width: none; width: 100%; }
    /* phone: one compact line per client (name, state, usage); a tap opens the dialog */
    .list thead { display: none; }
    .list, .list tbody { display: block; }
    .list tr { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 4px var(--space-3); padding: var(--space-3) var(--space-4); border-top: 1px solid var(--line); }
    .list td, .list tr.def td { display: none; padding: 0; border: 0 !important; background: none !important; }
    .list tr.def { background: color-mix(in srgb, var(--accent-soft) 55%, transparent); }
    .list tr.def td.who { display: flex; }
    .list tr.def td.actions { display: block; }
    .list td.who { display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap; }
    .list td.use { display: block; grid-column: 1 / -1; }
    .list tr.def td.c-speed, .list tr.def td.c-quota { display: block; grid-column: 1 / -1; font-size: var(--step--1); }
    .list tr.def td.use { display: none; }
    .list td.actions { display: block; grid-row: 1; grid-column: 2; }
    .list td.actions .btn.ghost:not(:last-child) { display: none; }
    .m-only { display: inline-flex; }
    .list .bar { max-width: none; }
    .list td.use { width: auto; }
    .indent { padding-left: 0; }
  }
</style>
