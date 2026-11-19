<script>
  import { api, poll, reloadServers } from '../lib/api.js';
  import { navigate, selectServer, servers, toast } from '../lib/state.svelte.js';
  import { bytes } from '../lib/format.js';
  import Dialog from '../lib/Dialog.svelte';
  import { machines, typeNames, where } from '../lib/location.js';

  // Every server on every machine, with start / stop, autostart and remove.
  let list = $state(null);
  let loadError = $state('');
  let busy = $state({}); // id → true while an action runs
  let removing = $state({ open: false, sv: null, busy: false });
  let stopping = $state({ open: false, sv: null });

  async function load() {
    try {
      list = await api('/servers');
      loadError = '';
    } catch (e) {
      loadError = e.message;
    }
  }
  $effect(() => poll(load, 10_000));

  // Scheduled backups at a glance (details on the Account page).
  let backups = $state(null);
  $effect(() => {
    api('/backups').then((b) => (backups = b)).catch(() => {});
  });
  const when = (t) => new Date(t).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });

  const typeLabel = (s) =>
    s.type === 'openvpn' ? 'OpenVPN' : s.type === 'amneziawg' ? `AmneziaWG ${s.obfuscation?.level ?? ''}` : 'WireGuard';

  function serverState(s) {
    if (s.stopped) return { key: 'off', label: 'Stopped' };
    if (s.type === 'openvpn') {
      if (!s.pki_ready) return { key: 'warn', label: 'Not set up' };
      return s.openvpn_version ? { key: 'ok', label: 'Running' } : { key: 'bad', label: 'Not running', why: s.mgmt_error };
    }
    return s.running ? { key: 'ok', label: 'Running' } : { key: 'bad', label: 'Not running', why: s.error };
  }

  // Groups: machine › node, the own node first (also nodes that do not answer).
  const groups = $derived.by(() => {
    if (!list) return [];
    return machines().flatMap((m) => m.nodes.map((n) => ({ key: n.key, node: n, items: list.filter((s) => (s.host ?? '') === n.key) })));
  });

  function open(s, path = '/') {
    selectServer(s.id);
    navigate(path);
  }

  async function act(s, what) {
    busy = { ...busy, [s.id]: true };
    try {
      const r = await api(`/servers/${encodeURIComponent(s.id)}/${what}`, { method: 'POST' });
      toast(r.note || `${s.name} ${what === 'start' ? 'started' : 'stopped'}.`);
      await Promise.all([load(), reloadServers()]);
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      busy = { ...busy, [s.id]: false };
    }
  }

  async function autostart(s, on) {
    try {
      await api(`/servers/${encodeURIComponent(s.id)}/autostart`, { method: 'PUT', body: { autostart: on } });
      toast(on ? `${s.name} starts with the VPN container again.` : `${s.name} stays stopped after a restart until you start it.`);
      await load();
    } catch (e) {
      toast(e.message, 'bad');
    }
  }

  const removable = (s) => !(s.type === 'openvpn' && (s.id === 'openvpn' || s.id.endsWith('.openvpn')));

  async function remove() {
    removing.busy = true;
    try {
      await api(`/servers/${encodeURIComponent(removing.sv.id)}`, { method: 'DELETE' });
      toast(`Removed ${removing.sv.name}.`);
      removing.open = false;
      await Promise.all([load(), reloadServers()]);
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      removing.busy = false;
    }
  }
</script>

<header class="head">
  <div>
    <h1>Servers</h1>
    <p class="muted">Every VPN server on every node, grouped by machine › node.</p>
  </div>
  <div class="head-actions">
    <a class="btn" href="/servers/new?type=restore">Restore from a backup</a>
    <a class="btn primary" href="/servers/new">Add a server</a>
  </div>
</header>

{#if loadError}<div class="banner bad" role="alert">Could not load the servers: {loadError}</div>{/if}
{#if backups}
  {#if backups.last && !backups.last.ok}
    <div class="banner bad backup-line" role="alert">The last backup ({when(backups.last.time)}) failed: {backups.last.error}. <a href="/account">Backups</a></div>
  {:else if backups.schedule.every === 'off'}
    <p class="muted small backup-line">No scheduled backups. <a href="/account">Turn them on</a></p>
  {:else}
    <p class="muted small backup-line">
      Backups {backups.schedule.every === 'daily' ? 'every day' : 'every week'}{backups.last ? `, last ${when(backups.last.time)}` : ''}{backups.encrypted ? '' : ' (not encrypted)'} · <a href="/account">Backups</a>
    </p>
  {/if}
{/if}

{#each groups as g (g.key)}
  <section class="panel group">
    <div class="panel-head">
      <h2><span class="tag">Machine</span> {g.node.machine} <span class="sep">›</span> <span class="tag">Node</span> {g.node.name}</h2>
      {#if g.node.error}
        <span class="small bad">Not reachable: {g.node.error}</span>
      {:else if g.node.containers}
        <span class="small muted">Runs {Object.keys(g.node.containers).map((t) => typeNames[t] ?? t).join(', ')}</span>
      {/if}
    </div>
    {#if !list}
      <p class="panel-body muted">Loading…</p>
    {:else if g.items.length === 0}
      <p class="panel-body muted">{g.node.error ? 'Its servers are not shown while it does not answer.' : 'No servers.'}</p>
    {:else}
      <div class="table-wrap">
        <table class="servers">
          <thead>
            <tr>
              <th>Server</th>
              <th>Type · runs in</th>
              <th>Port</th>
              <th>State</th>
              <th class="num">Clients</th>
              <th class="num">Connected</th>
              <th class="num">Today</th>
              <th>Autostart</th>
              <th><span class="sr">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {#each g.items as s (s.id)}
              {@const st = serverState(s)}
              <tr>
                <td data-label="Server">
                  <button class="link" onclick={() => open(s)}><strong>{s.name}</strong></button>
                  <span class="mono faint small id">{s.id}{s.subnet ? ` · ${s.subnet}` : ''}</span>
                </td>
                <td data-label="Type">
                  {typeLabel(s)}
                  {#if s.container}<span class="mono faint small block nowrap" title="The container that runs it">{s.container}</span>{/if}
                </td>
                <td data-label="Port" class="mono nowrap">{s.listen_port ? `${(s.proto || 'udp').toUpperCase()} ${s.listen_port}` : '–'}</td>
                <td data-label="State">
                  <span class="pill {st.key}" title={st.why ?? ''}>{st.label}</span>
                  {#if st.why}<span class="faint small why">{st.why}</span>{/if}
                </td>
                <td data-label="Clients" class="num">{s.clients}</td>
                <td data-label="Connected" class="num">{s.stopped ? '–' : s.connected}</td>
                <td data-label="Today" class="num mono small">↓{bytes(s.today_out)} ↑{bytes(s.today_in)}</td>
                <td data-label="Autostart">
                  <label class="switch" title="Start with the VPN container (after a restart of the container or the machine)">
                    <input type="checkbox" checked={s.autostart} onchange={(e) => autostart(s, e.currentTarget.checked)} />
                    <span>{s.autostart ? 'On' : 'Off'}</span>
                  </label>
                </td>
                <td class="actions">
                  {#if s.stopped}
                    <button class="btn small primary" disabled={busy[s.id]} onclick={() => act(s, 'start')}>{busy[s.id] ? 'Starting…' : 'Start'}</button>
                  {:else}
                    <button class="btn small" disabled={busy[s.id]} onclick={() => (stopping = { open: true, sv: s })}>{busy[s.id] ? 'Stopping…' : 'Stop'}</button>
                  {/if}
                  <button class="btn small ghost" onclick={() => open(s, s.type === 'openvpn' ? '/server' : '/')} title="Backup is on the server's page">Backup</button>
                  {#if removable(s)}
                    <button class="btn small ghost danger" onclick={() => (removing = { open: true, sv: s, busy: false })}>Remove</button>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>
{/each}

<p class="muted small foot">
  <a href="/nodes">Nodes</a> · Stopped servers stay stopped until you start them. Autostart off: a server stays stopped after the
  VPN container or the machine restarts.
</p>

<Dialog bind:open={stopping.open} title="Stop {stopping.sv?.name}?">
  <p><strong>{stopping.sv?.name}</strong> ({typeLabel(stopping.sv ?? {})}, id <span class="mono">{stopping.sv?.id}</span>) on <strong>{where(stopping.sv)}</strong> stops: its clients are disconnected until you start it again.</p>
  {#snippet actions()}
    <button class="btn" onclick={() => (stopping.open = false)}>Cancel</button>
    <button class="btn primary" onclick={() => { stopping.open = false; act(stopping.sv, 'stop'); }}>Stop server</button>
  {/snippet}
</Dialog>

<Dialog bind:open={removing.open} title="Remove {removing.sv?.name}?">
  <p><strong>{removing.sv?.name}</strong> ({typeLabel(removing.sv ?? {})}, id <span class="mono">{removing.sv?.id}</span>) on <strong>{where(removing.sv)}</strong>.</p>
  <p>
    {removing.sv?.type === 'openvpn'
      ? 'OpenVPN stops; its certificates and settings are kept in a .removed folder.'
      : 'The server stops and its clients and keys are deleted.'} Devices using it can no longer connect. Traffic history stays.
  </p>
  {#snippet actions()}
    <button class="btn" onclick={() => (removing.open = false)}>Cancel</button>
    <button class="btn danger solid" disabled={removing.busy} onclick={remove}>{removing.busy ? 'Removing…' : 'Remove server'}</button>
  {/snippet}
</Dialog>

<style>
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-3); flex-wrap: wrap; margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }
  .head-actions { display: flex; gap: var(--space-2); flex-wrap: wrap; }
  .group { margin-bottom: var(--space-5); }
  .backup-line { margin: calc(-1 * var(--space-3)) 0 var(--space-4); }
  .panel-head { display: flex; justify-content: space-between; align-items: baseline; gap: var(--space-3); }
  .bad { color: var(--bad); }
  .tag { font-size: var(--step--1); font-weight: 500; color: var(--ink-3); text-transform: uppercase; letter-spacing: 0.04em; }
  .sep { color: var(--ink-3); margin: 0 4px; }
  .servers td { vertical-align: middle; }
  .servers .num { text-align: right; white-space: nowrap; }
  .nowrap { white-space: nowrap; }
  .block { display: block; }
  .id { display: block; }
  .why { display: block; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .link { background: none; border: 0; padding: 0; color: var(--ink); font: inherit; cursor: pointer; text-align: left; }
  .link:hover strong { text-decoration: underline; }
  .actions { white-space: nowrap; text-align: right; }
  .actions .btn { margin-left: var(--space-1); }
  .btn.danger { color: var(--bad); }
  .switch { display: inline-flex; align-items: center; gap: var(--space-2); cursor: pointer; }
  .foot { margin-top: var(--space-3); }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }

  /* Phones: each server is a card */
  @media (max-width: 760px) {
    .servers thead { display: none; }
    .servers, .servers tbody, .servers tr, .servers td { display: block; }
    .servers tr { padding: var(--space-3) var(--space-4); border-bottom: 1px solid var(--line); }
    .servers td { padding: 2px 0; border: 0; text-align: left; }
    .servers td[data-label]:not([data-label='Server'])::before { content: attr(data-label) ': '; color: var(--ink-3); font-size: var(--step--1); }
    .servers .num { text-align: left; }
    .actions { text-align: left; padding-top: var(--space-2) !important; }
    .actions .btn:first-child { margin-left: 0; }
  }
</style>
