<script>
  import { onMount } from 'svelte';
  import { api, reloadServers } from '../lib/api.js';
  import { toast, route, setQuery, navigate, servers, selectServer, currentServer } from '../lib/state.svelte.js';
  import { dateTime } from '../lib/format.js';
  import { lineDiff } from '../lib/diff.js';
  import Dialog from '../lib/Dialog.svelte';
  import ServerBackup from '../lib/ServerBackup.svelte';

  let cfg = $state(null); // last loaded server config
  let net = $state(null); // client LANs (for route suggestions)
  let loadError = $state('');
  let tab = $state(route.query.get('tab') ?? 'settings');
  $effect(() => setQuery({ tab: tab === 'settings' ? '' : tab }));

  let form = $state(null); // settings being edited
  let raw = $state(''); // raw text being edited
  let preview = $state(null); // { raw, problems, diff, source }
  let busy = $state(false);
  let saveError = $state('');

  // Servers added in trapdoor can be removed; the first one cannot.
  const removable = $derived(!!servers.current && servers.current !== 'openvpn');
  let remove = $state({ open: false, busy: false });
  async function doRemove() {
    const id = servers.current;
    remove.busy = true;
    try {
      await api('/servers/' + encodeURIComponent(id), { method: 'DELETE' });
      await reloadServers();
      const next = servers.list.find((s) => s.id !== id);
      if (next) selectServer(next.id);
      toast('Server removed.');
      remove.open = false;
      navigate('/');
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      remove.busy = false;
    }
  }

  async function load() {
    try {
      cfg = await api('/server/config');
      form = structuredClone($state.snapshot(cfg.settings));
      raw = cfg.raw;
      loadError = '';
    } catch (e) {
      loadError = e.message;
    }
    try {
      net = await api('/network');
    } catch {
      net = null;
    }
  }
  onMount(load);

  const dirtySettings = $derived(cfg && form && JSON.stringify(form) !== JSON.stringify(cfg.settings));
  const dirtyRaw = $derived(cfg && raw !== cfg.raw);

  // LANs behind clients that have no server route yet.
  const missingRoutes = $derived.by(() => {
    if (!net || !form) return [];
    const out = [];
    for (const [client, lans] of Object.entries(net.client_lans ?? {})) {
      for (const l of lans) if (!form.routes.includes(l)) out.push({ client, lan: l });
    }
    return out;
  });

  async function check(source) {
    busy = true;
    saveError = '';
    try {
      const body = source === 'raw' ? { raw } : { settings: form };
      const r = await api('/server/config/check', { method: 'POST', body });
      preview = { ...r, diff: lineDiff(cfg.raw, r.raw), source };
    } catch (e) {
      saveError = e.message;
    } finally {
      busy = false;
    }
  }

  async function save() {
    busy = true;
    saveError = '';
    try {
      const body = preview.source === 'raw' ? { raw: preview.raw, base_version: cfg.version } : { settings: form, base_version: cfg.version };
      cfg = await api('/server/config', { method: 'PUT', body });
      form = structuredClone($state.snapshot(cfg.settings));
      raw = cfg.raw;
      preview = null;
      toast('server.conf saved. Restart OpenVPN to use it.');
    } catch (e) {
      saveError = e.status === 409 ? 'server.conf was changed by someone else. Reload to see the current file.' : e.message;
    } finally {
      busy = false;
    }
  }

  // Restart
  let restart = $state({ open: false, busy: false, result: null });
  async function doRestart() {
    restart.busy = true;
    restart.result = null;
    try {
      restart.result = await api('/server/restart', { method: 'POST', body: { rollback: true } });
      if (restart.result.ok) toast(`OpenVPN restarted in ${Math.round(restart.result.seconds)} s.`);
      await load();
    } catch (e) {
      restart.result = { ok: false, error: e.message };
    } finally {
      restart.busy = false;
    }
  }

  // Backups
  let backups = $state([]);
  let viewing = $state({ open: false, name: '', raw: '' });
  $effect(() => {
    if (tab !== 'backups') return;
    api('/server/backups').then((b) => (backups = b)).catch((e) => toast(e.message, 'bad'));
  });
  async function view(b) {
    try {
      const r = await api(`/server/backups/${b.name}`);
      viewing = { open: true, name: b.name, raw: r.raw };
    } catch (e) {
      toast(e.message, 'bad');
    }
  }
  function useBackup() {
    raw = viewing.raw;
    viewing.open = false;
    tab = 'raw';
    toast('Backup loaded into the editor. Check and save it to restore.');
  }

  // List editing helpers
  let add = $state({ dns: '', routes: '', push_routes: '' });
  function addTo(key, pattern, msg) {
    const v = add[key].trim();
    if (!v) return;
    if (!pattern.test(v)) {
      toast(msg, 'bad');
      return;
    }
    if (!form[key].includes(v)) form[key] = [...form[key], v];
    add[key] = '';
  }
  const ipRe = /^[0-9a-fA-F:.]+$/;
  const cidrRe = /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/;
  const portChanged = $derived(cfg && form && (form.port !== cfg.settings.port || form.proto !== cfg.settings.proto));
</script>

<header class="head">
  <div>
    <h1>Server</h1>
    <p class="muted">OpenVPN's server.conf. Changes are checked by OpenVPN before they are saved.</p>
  </div>
  <button class="btn" onclick={() => (restart = { open: true, busy: false, result: null })}>Restart OpenVPN</button>
</header>

{#if loadError}<div class="banner bad" role="alert">Could not load server.conf: {loadError}</div>{/if}

{#if cfg?.restart_pending}
  <div class="banner warn pending">
    <span>Saved changes are not active yet: OpenVPN is still running with the previous config.</span>
    <button class="btn small primary" onclick={() => (restart = { open: true, busy: false, result: null })}>Apply and restart</button>
  </div>
{/if}

{#if cfg && cfg.problems.length}
  <ul class="problems">
    {#each cfg.problems as p (p.message)}
      <li class:err={p.error}><span class="pill {p.error ? 'bad' : 'warn'}">{p.error ? 'Error' : 'Note'}</span> {p.message}</li>
    {/each}
  </ul>
{/if}

<div class="tabs" role="tablist">
  {#each [['settings', 'Settings'], ['raw', 'server.conf'], ['backups', 'Backups']] as [key, label] (key)}
    <button role="tab" aria-selected={tab === key} onclick={() => (tab = key)}>{label}</button>
  {/each}
</div>

{#if form && tab === 'settings'}
  <form class="panel settings" onsubmit={(e) => { e.preventDefault(); check('settings'); }}>
    <section>
      <h2>Connection</h2>
      <div class="grid">
        <label class="field"><span>Port</span><input type="number" min="1" max="65535" bind:value={form.port} /></label>
        <label class="field">
          <span>Protocol</span>
          <select bind:value={form.proto}>
            <option value="udp">UDP (recommended)</option>
            <option value="tcp">TCP</option>
            <option value="udp6">UDP over IPv6</option>
            <option value="tcp6">TCP over IPv6</option>
          </select>
        </label>
        <label class="field"><span>Max clients</span><input type="number" min="0" bind:value={form.max_clients} /><span class="hint">0 = no limit</span></label>
      </div>
      {#if portChanged}
        <p class="banner warn">Client profiles contain the port and protocol: update <code>client-template.txt</code>, the port mapping, and send new profiles to every device.</p>
      {/if}
    </section>

    <section>
      <h2>VPN network</h2>
      <div class="grid">
        <label class="field"><span>Subnet</span><input bind:value={form.subnet} placeholder="10.8.0.0/24" /><span class="hint">Changing it moves every client to new addresses.</span></label>
        <label class="field">
          <span>Topology</span>
          <select bind:value={form.topology}>
            <option value="subnet">subnet (recommended)</option>
            <option value="net30">net30 (old default)</option>
            <option value="p2p">p2p</option>
          </select>
        </label>
      </div>
      <label class="check"><input type="checkbox" bind:checked={form.full_tunnel} /> Send all internet traffic of every client through the VPN</label>
      <label class="check"><input type="checkbox" bind:checked={form.client_to_client} /> Clients can reach each other</label>
      <label class="check"><input type="checkbox" bind:checked={form.duplicate_cn} /> Allow one certificate on several devices at the same time</label>
    </section>

    <section>
      <h2>DNS for clients</h2>
      <div class="chips">
        {#each form.dns as d (d)}<span class="chip mono">{d}<button type="button" aria-label="Remove {d}" onclick={() => (form.dns = form.dns.filter((x) => x !== d))}>×</button></span>{/each}
      </div>
      <div class="add">
        <input bind:value={add.dns} placeholder="1.1.1.1" aria-label="Add a DNS server" onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), addTo('dns', ipRe, 'Enter an IP address'))} />
        <button type="button" class="btn small" onclick={() => addTo('dns', ipRe, 'Enter an IP address')}>Add</button>
      </div>
      <label class="field narrow"><span>Search domain</span><input bind:value={form.domain} placeholder="home.lan" /></label>
    </section>

    <section>
      <h2>Routes</h2>
      <h3>Networks behind clients</h3>
      <p class="hint">The server sends traffic for these networks into the VPN (<code>route</code>). Each also needs a client with it under "Networks behind this client".</p>
      <div class="chips">
        {#each form.routes as r (r)}<span class="chip mono">{r}<button type="button" aria-label="Remove {r}" onclick={() => (form.routes = form.routes.filter((x) => x !== r))}>×</button></span>{/each}
      </div>
      {#if missingRoutes.length}
        <div class="suggest">
          {#each missingRoutes as m (m.lan)}
            <button type="button" class="btn small" onclick={() => (form.routes = [...form.routes, m.lan])}>Add {m.lan} (behind {m.client})</button>
          {/each}
        </div>
      {/if}
      <div class="add">
        <input bind:value={add.routes} placeholder="192.168.50.0/24" aria-label="Add a network behind a client" onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), addTo('routes', cidrRe, 'Enter a network like 192.168.50.0/24'))} />
        <button type="button" class="btn small" onclick={() => addTo('routes', cidrRe, 'Enter a network like 192.168.50.0/24')}>Add</button>
      </div>

      <h3>Networks every client can reach</h3>
      <p class="hint">Sent to all clients as routes (<code>push "route …"</code>), e.g. the server's LAN.</p>
      <div class="chips">
        {#each form.push_routes as r (r)}<span class="chip mono">{r}<button type="button" aria-label="Remove {r}" onclick={() => (form.push_routes = form.push_routes.filter((x) => x !== r))}>×</button></span>{/each}
      </div>
      <div class="add">
        <input bind:value={add.push_routes} placeholder="192.168.1.0/24" aria-label="Add a network for all clients" onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), addTo('push_routes', cidrRe, 'Enter a network like 192.168.1.0/24'))} />
        <button type="button" class="btn small" onclick={() => addTo('push_routes', cidrRe, 'Enter a network like 192.168.1.0/24')}>Add</button>
      </div>
    </section>

    {#if saveError}<div class="banner bad" role="alert">{saveError}</div>{/if}
    <div class="actions">
      <button type="button" class="btn" disabled={!dirtySettings || busy} onclick={() => (form = structuredClone($state.snapshot(cfg.settings)))}>Discard</button>
      <button type="submit" class="btn primary" disabled={!dirtySettings || busy}>{busy ? 'Checking…' : 'Review changes'}</button>
    </div>
  </form>
{/if}

{#if cfg && tab === 'raw'}
  <div class="panel rawbox">
    <textarea bind:value={raw} spellcheck="false" aria-label="server.conf" rows="28"></textarea>
    {#if saveError}<div class="banner bad" role="alert">{saveError}</div>{/if}
    <div class="actions">
      <button class="btn" disabled={!dirtyRaw || busy} onclick={() => (raw = cfg.raw)}>Discard</button>
      <button class="btn primary" disabled={!dirtyRaw || busy} onclick={() => check('raw')}>{busy ? 'Checking…' : 'Review changes'}</button>
    </div>
  </div>
{/if}

{#if tab === 'backups'}
  <section class="panel">
    {#if backups.length === 0}
      <p class="empty">No backups yet. Every save keeps the previous file (the last 10).</p>
    {:else}
      <table>
        <thead><tr><th>Saved over</th><th class="num">Size</th><th></th></tr></thead>
        <tbody>
          {#each backups as b (b.name)}
            <tr>
              <td>{dateTime(b.time)} <span class="mono faint small">{b.name}</span></td>
              <td class="num">{b.size} B</td>
              <td class="act"><button class="btn small" onclick={() => view(b)}>View</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>
{/if}

<ServerBackup />

{#if removable}
  <section class="panel remove">
    <div class="panel-head"><h2>Remove server</h2></div>
    <div class="panel-body remove-row">
      <p class="muted">Stops this OpenVPN server. Its clients can no longer connect. Its certificates and settings are kept in a folder named <code>.removed</code> next to it; traffic history stays.</p>
      <button class="btn danger" onclick={() => (remove = { open: true, busy: false })}>Remove server</button>
    </div>
  </section>
{/if}

<Dialog bind:open={remove.open} title="Remove this server?">
  <p><strong>{currentServer()?.name}</strong> stops, and devices using it can no longer connect.</p>
  {#snippet actions()}
    <button class="btn" onclick={() => (remove.open = false)}>Cancel</button>
    <button class="btn danger solid" disabled={remove.busy} onclick={doRemove}>{remove.busy ? 'Removing…' : 'Remove server'}</button>
  {/snippet}
</Dialog>

<Dialog bind:open={() => preview !== null, (v) => !v && (preview = null)} title="Review changes to server.conf">
  {#if preview}
    {#if preview.problems.length}
      <ul class="problems in-dialog">
        {#each preview.problems as p (p.message)}
          <li><span class="pill {p.error ? 'bad' : 'warn'}">{p.error ? 'Error' : 'Note'}</span> {p.message}</li>
        {/each}
      </ul>
    {/if}
    {#if preview.diff.length === 0}
      <p class="muted">No changes.</p>
    {:else}
      <pre class="diff">{#each preview.diff as l, i (i)}<span class="d{l.op === '+' ? 'add' : l.op === '-' ? 'del' : l.op === '…' ? 'gap' : 'eq'}">{l.op === '…' ? '  …' : l.op + ' ' + l.text}</span>
{/each}</pre>
    {/if}
    {#if saveError}<div class="banner bad" role="alert">{saveError}</div>{/if}
    <p class="hint">Saving does not restart OpenVPN; clients stay connected until you apply.</p>
  {/if}
  {#snippet actions()}
    <button class="btn" onclick={() => (preview = null)}>Back</button>
    <button class="btn primary" disabled={busy || !preview || preview.diff.length === 0 || preview.problems.some((p) => p.error)} onclick={save}>{busy ? 'Saving…' : 'Save'}</button>
  {/snippet}
</Dialog>

<Dialog bind:open={restart.open} title="Restart OpenVPN?">
  {#if restart.result}
    {#if restart.result.ok}
      <p class="banner ok-banner">OpenVPN is running again ({restart.result.openvpn_version?.split(' ').slice(0, 2).join(' ')}). Clients reconnect on their own.</p>
    {:else}
      <p class="banner bad">{restart.result.error}</p>
    {/if}
  {:else}
    <p>Every client is disconnected and reconnects within a few seconds.</p>
    <p class="muted small">If OpenVPN does not come back within 30 seconds, the config it ran with before is restored and it is started again.</p>
    {#if cfg?.restart_method === 'SIGHUP'}
      <p class="hint">Restart uses SIGHUP (OpenVPN re-reads its config). If it drops privileges (user nobody), some changes need a full service restart.</p>
    {/if}
  {/if}
  {#snippet actions()}
    {#if restart.result}
      <button class="btn" onclick={() => (restart.open = false)}>Close</button>
    {:else}
      <button class="btn" onclick={() => (restart.open = false)}>Cancel</button>
      <button class="btn primary" disabled={restart.busy} onclick={doRestart}>{restart.busy ? 'Restarting…' : 'Restart'}</button>
    {/if}
  {/snippet}
</Dialog>

<Dialog bind:open={viewing.open} title="Backup {viewing.name}">
  <pre class="diff">{viewing.raw}</pre>
  {#snippet actions()}
    <button class="btn" onclick={() => (viewing.open = false)}>Close</button>
    <button class="btn primary" onclick={useBackup}>Load into editor</button>
  {/snippet}
</Dialog>

<style>
  .remove { margin-top: var(--space-5); }
  .remove-row { display: flex; gap: var(--space-4); align-items: center; justify-content: space-between; }
  @media (max-width: 600px) { .remove-row { flex-direction: column; align-items: stretch; } }
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-4); margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }
  .banner { margin-bottom: var(--space-3); }
  .pending { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); }
  .problems { list-style: none; margin: 0 0 var(--space-4); padding: 0; display: grid; gap: 6px; font-size: var(--step--1); }
  .problems li { display: flex; gap: var(--space-2); align-items: baseline; }
  .problems.in-dialog { margin: 0; }

  .tabs { display: flex; gap: 2px; margin-bottom: var(--space-3); }
  .tabs button { height: 32px; padding: 0 var(--space-3); border: 0; border-radius: var(--radius-s); background: transparent; color: var(--ink-2); font: inherit; font-weight: 500; cursor: pointer; }
  .tabs button:hover { background: var(--sunken); color: var(--ink); }
  .tabs button[aria-selected='true'] { background: var(--surface); color: var(--ink); box-shadow: inset 0 0 0 1px var(--line); }

  .settings { display: grid; }
  .settings section { display: grid; gap: var(--space-3); padding: var(--space-5); border-bottom: 1px solid var(--line); }
  .settings h3 { margin-top: var(--space-2); font-size: var(--step--1); color: var(--ink-2); font-weight: 500; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: var(--space-3); }
  .narrow { max-width: 280px; }
  .check { display: flex; gap: var(--space-2); align-items: center; }
  .check input { height: auto; }
  .chips { display: flex; flex-wrap: wrap; gap: 6px; }
  .chips:empty { display: none; }
  .chip { display: inline-flex; align-items: center; gap: 4px; padding: 2px 4px 2px 8px; border-radius: var(--radius-s); background: var(--sunken); font-size: 13px; }
  .chip button { width: 20px; height: 20px; border: 0; border-radius: 3px; background: none; color: var(--ink-2); font-size: 15px; line-height: 1; cursor: pointer; }
  .chip button:hover { background: var(--line); color: var(--ink); }
  .add { display: flex; gap: var(--space-2); max-width: 420px; }
  .add input { flex: 1; }
  .suggest { display: flex; flex-wrap: wrap; gap: var(--space-2); }
  .actions { display: flex; justify-content: flex-end; gap: var(--space-2); padding: var(--space-4) var(--space-5); }
  .settings .banner { margin: 0; }

  .rawbox { display: grid; }
  textarea {
    width: 100%;
    min-height: 420px;
    padding: var(--space-4);
    border: 0;
    border-bottom: 1px solid var(--line);
    border-radius: var(--radius-m) var(--radius-m) 0 0;
    background: var(--surface);
    color: var(--ink);
    font-family: var(--mono);
    font-size: 13px;
    line-height: 1.55;
    resize: vertical;
  }
  .rawbox .banner { margin: var(--space-3) var(--space-5) 0; }

  .diff {
    margin: 0;
    max-height: 50vh;
    overflow: auto;
    padding: var(--space-3);
    border-radius: var(--radius-s);
    background: var(--sunken);
    font-family: var(--mono);
    font-size: 12.5px;
    line-height: 1.5;
    white-space: pre-wrap;
    word-break: break-all;
  }
  .dadd { color: var(--ok); background: var(--ok-soft); display: inline-block; width: 100%; }
  .ddel { color: var(--bad); background: var(--bad-soft); display: inline-block; width: 100%; text-decoration: line-through; text-decoration-color: color-mix(in srgb, var(--bad) 40%, transparent); }
  .deq { color: var(--ink-3); }
  .dgap { color: var(--ink-3); }
  .ok-banner { background: var(--ok-soft); color: var(--ok); }
  td.act { text-align: right; }
  code { font-family: var(--mono); font-size: 12px; }
</style>
