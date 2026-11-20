<script>
  import { onMount } from 'svelte';
  import { api, apiURL, poll } from '../lib/api.js';
  import { date, relative, certState } from '../lib/format.js';
  import { toast, route, setQuery, settings } from '../lib/state.svelte.js';
  import Dialog from '../lib/Dialog.svelte';
  import TwoFactor from '../lib/TwoFactor.svelte';
  import NetworkDialog from '../lib/NetworkDialog.svelte';
  import NamedNetworks from '../lib/NamedNetworks.svelte';
  import { placeMenu } from '../lib/menu.js';
  import SendProfile from '../lib/SendProfile.svelte';
  import Lifetime from '../lib/Lifetime.svelte';

  let twoFactor = $state({ open: false, name: '' });
  let network = $state({ open: false, name: '' });
  let namedOpen = $state(false);
  let staticIPs = $state({});
  let emails = $state({}); // client → email address
  let mailing = $state({ open: false, name: '' });

  let certs = $state([]);
  let notify = $state({}); // name → true: online / offline messages on
  let online = $state(new Set());
  let loadError = $state('');
  let loaded = $state(false);

  let search = $state(route.query.get('q') ?? '');
  let filter = $state(route.query.get('show') ?? 'all');
  let showHistory = $state(false);

  async function setNotify(name, on) {
    try {
      await api(`/notify/${encodeURIComponent(name)}`, { method: 'PUT', body: { on } });
      notify = { ...notify, [name]: on || undefined };
      toast(on ? 'Online / offline messages on.' : 'Online / offline messages off.');
    } catch (e) {
      toast(e.message, 'bad');
    }
  }

  async function load() {
    try {
      certs = await api('/certs');
      loadError = '';
    } catch (e) {
      loadError = e.message;
    } finally {
      loaded = true;
    }
    api('/emails').then((m) => (emails = m)).catch(() => {});
    api('/notify').then((m) => (notify = m)).catch(() => {});
    try {
      staticIPs = (await api('/network')).static_ips ?? {};
    } catch {
      staticIPs = {};
    }
    try {
      const live = await api('/sessions');
      online = new Set(live.sessions.map((s) => s.name));
    } catch {
      online = new Set();
    }
  }
  onMount(() => poll(load, 20000));

  $effect(() => setQuery({ q: search, show: filter === 'all' ? '' : filter }));

  const filters = [
    { key: 'all', label: 'All' },
    { key: 'valid', label: 'Valid' },
    { key: 'expiring', label: 'Expire soon' },
    { key: 'expired', label: 'Expired' },
    { key: 'revoked', label: 'Revoked' },
  ];

  // Server certificates are not clients; they are managed with the server setup.
  const clientCerts = $derived(certs.filter((c) => c.kind !== 'server'));

  const rows = $derived(
    clientCerts
      .filter((c) => showHistory || c.current)
      .map((c) => ({ ...c, st: certState(c, settings.expiryWarnDays) }))
      .filter((c) => filter === 'all' || c.st.key === filter)
      .filter((c) => !search || `${c.name} ${c.notes ?? ''}`.toLowerCase().includes(search.toLowerCase()))
      .sort((a, b) => a.name.localeCompare(b.name) || (b.current - a.current)),
  );
  const counts = $derived.by(() => {
    const n = { all: 0, valid: 0, expiring: 0, expired: 0, revoked: 0 };
    for (const c of clientCerts.filter((c) => c.current)) {
      n.all++;
      n[certState(c, settings.expiryWarnDays).key]++;
    }
    return n;
  });

  // Clients a named network can be given to: every current, not revoked one (whatever the filter).
  const namedClients = $derived(clientCerts.filter((c) => c.current && c.status !== 'revoked').map((c) => c.name).sort());

  // Selection for bulk renew: current, not revoked.
  let selected = $state(new Set());
  const selectable = $derived(rows.filter((c) => c.current && c.status !== 'revoked'));
  const allSelected = $derived(selectable.length > 0 && selectable.every((c) => selected.has(c.name)));
  function toggle(name) {
    const s = new Set(selected);
    s.has(name) ? s.delete(name) : s.add(name);
    selected = s;
  }
  function toggleAll() {
    selected = allSelected ? new Set() : new Set(selectable.map((c) => c.name));
  }
  function selectAttention() {
    filter = 'all';
    selected = new Set(
      clientCerts
        .filter((c) => c.current && ['expiring', 'expired'].includes(certState(c, settings.expiryWarnDays).key))
        .map((c) => c.name),
    );
  }

  // The server's lifetime for new and renewed certificates (0 = 825 days).
  let defaultDays = $state(825);
  async function loadDefaults() {
    try {
      defaultDays = (await api('/cert-defaults')).validity_days || 825;
    } catch {
      defaultDays = 825;
    }
  }
  onMount(loadDefaults);

  let bulk = $state({ open: false, validity: 825, busy: false, results: null });
  async function renewSelected() {
    bulk.busy = true;
    try {
      const res = await api('/certs/renew', { method: 'POST', body: { names: [...selected], validity_days: Number(bulk.validity) } });
      bulk.results = res;
      const ok = res.filter((r) => r.ok).length;
      toast(`Renewed ${ok} of ${res.length}. Each device needs its new profile.`, ok === res.length ? 'ok' : 'bad');
      selected = new Set(res.filter((r) => !r.ok).map((r) => r.name));
      await load();
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      bulk.busy = false;
    }
  }

  // Create
  let createOpen = $state(false);
  let form = $state({ name: '', notes: '', passphrase: '', validity: 825, makeDefault: false });
  let presetLife = $state(true);
  let showPass = $state(false);
  let busy = $state(false);
  let formError = $state('');
  const nameOk = $derived(/^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$/.test(form.name) && form.name !== 'ca');

  function openCreate() {
    form = { name: '', notes: '', passphrase: '', validity: defaultDays, makeDefault: false };
    formError = '';
    showPass = false;
    createOpen = true;
  }

  async function create(e) {
    e.preventDefault();
    busy = true;
    formError = '';
    try {
      const body = { name: form.name, validity_days: Number(form.validity) };
      if (form.passphrase) body.passphrase = form.passphrase;
      if (form.notes.trim()) body.notes = form.notes.trim();
      await api('/certs', { method: 'POST', body });
      if (form.makeDefault && presetLife && Number(form.validity) !== defaultDays) {
        await api('/cert-defaults', { method: 'PUT', body: { validity_days: Number(form.validity) } });
        await loadDefaults();
      }
      createOpen = false;
      toast(`Created ${form.name}. Download the profile to set up the device.`);
      await load();
    } catch (err) {
      formError = err.message;
    } finally {
      busy = false;
    }
  }

  // Readable name ("Anna's phone"); the certificate name stays the login.
  let naming = $state({ open: false, name: '', notes: '', busy: false, error: '' });
  function openNaming(name) {
    naming = { open: true, name, notes: certs.find((c) => c.name === name && c.current)?.notes ?? '', busy: false, error: '' };
  }
  async function saveNaming(e) {
    e.preventDefault();
    naming.busy = true;
    naming.error = '';
    try {
      await api(`/certs/${encodeURIComponent(naming.name)}/notes`, { method: 'PUT', body: { notes: naming.notes.trim() } });
      naming.open = false;
      toast(naming.notes.trim() ? `Renamed ${naming.name} to “${naming.notes.trim()}”.` : `Removed the display name of ${naming.name}.`);
      await load();
    } catch (err) {
      naming.error = err.message;
    } finally {
      naming.busy = false;
    }
  }

  // Row actions
  let confirm = $state({ open: false, kind: '', name: '' });
  async function act(kind, name) {
    const calls = {
      revoke: ['POST', `/certs/${name}/revoke`, `Revoked ${name} and disconnected it.`],
      unrevoke: ['POST', `/certs/${name}/unrevoke`, `Restored ${name}.`],
      renew: ['POST', `/certs/${name}/renew`, `Renewed ${name}. Send the new profile to the device.`],
      delete: ['DELETE', `/certs/${name}`, `Deleted ${name}.`],
      disconnect: ['DELETE', `/certs/${name}/sessions`, `Disconnected ${name}.`],
    };
    const [method, path, done] = calls[kind];
    try {
      await api(path, { method });
      toast(done);
      confirm.open = false;
      await load();
    } catch (err) {
      toast(err.message, 'bad');
    }
  }
  function ask(kind, name) {
    confirm = { open: true, kind, name };
  }
  const confirmText = {
    revoke: {
      title: 'Revoke this client?',
      body: 'The device is disconnected now and cannot connect again. You can restore it later.',
      button: 'Revoke',
    },
    delete: {
      title: 'Delete this client?',
      body: 'Its keys are deleted. The certificate stays blocked until it expires, so the old profile can never connect again. This cannot be undone.',
      button: 'Delete',
    },
    renew: {
      title: 'Renew this certificate?',
      body: 'A new certificate is issued with the same key. The old profile stops working: download the new one and install it on the device.',
      button: 'Renew',
    },
  };
</script>

<header class="head">
  <div>
    <h1>Clients</h1>
    <p class="muted">One certificate per device. Download its profile to connect.</p>
  </div>
  <div class="head-actions">
    <button class="btn" onclick={() => (namedOpen = true)}>Named networks</button>
    <button class="btn primary" onclick={openCreate}>New client</button>
  </div>
</header>

<div class="toolbar">
  <div class="tabs" role="tablist" aria-label="Filter by status">
    {#each filters as f (f.key)}
      <button role="tab" aria-selected={filter === f.key} onclick={() => (filter = f.key)}>
        {f.label}<span class="count">{counts[f.key]}</span>
      </button>
    {/each}
  </div>
  <input class="search" type="search" placeholder="Search by name" aria-label="Search by name" bind:value={search} />
</div>

{#if loadError}<div class="banner bad" role="alert">Could not load clients: {loadError}</div>{/if}

{#if counts.expiring + counts.expired > 0 && selected.size === 0}
  <div class="banner warn attention">
    <span>
      {counts.expiring + counts.expired} client {counts.expiring + counts.expired === 1 ? 'certificate needs' : 'certificates need'} renewing
      ({counts.expired ? `${counts.expired} expired, ` : ''}{counts.expiring} within {settings.expiryWarnDays} days).
    </span>
    <button class="btn small" onclick={selectAttention}>Select them</button>
  </div>
{/if}

{#if selected.size > 0}
  <div class="bulkbar" role="region" aria-label="Selected clients">
    <span><strong>{selected.size}</strong> selected</span>
    <button class="btn small primary" onclick={() => { bulk.results = null; bulk.validity = defaultDays; bulk.open = true; }}>Renew selected</button>
    <button class="btn small ghost" onclick={() => (selected = new Set())}>Clear</button>
  </div>
{/if}

<section class="panel">
  {#if loaded && rows.length === 0}
    <div class="empty">
      {#if clientCerts.length === 0}
        <p>No clients yet. Create the first one to get a profile for a phone or laptop.</p>
      {:else}
        <p>No clients match this filter.</p>
      {/if}
    </div>
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th class="sel"><input type="checkbox" checked={allSelected} disabled={selectable.length === 0} onchange={toggleAll} aria-label="Select all shown" /></th>
            <th>Name</th>
            <th>Status</th>
            <th>Valid until</th>
            <th class="hide-s">Serial</th>
            <th><span class="sr">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each rows as c (c.serial)}
            <tr class:old={!c.current} class:picked={selected.has(c.name) && c.current}>
              <td class="sel">
                {#if c.current && c.status !== 'revoked'}
                  <input type="checkbox" checked={selected.has(c.name)} onchange={() => toggle(c.name)} aria-label="Select {c.name}" />
                {/if}
              </td>
              <td class="c-name">
                <span class="name">
                  {#if online.has(c.name) && c.current}<i class="dot" title="Connected now"></i>{/if}
                  {c.notes || c.name}
                  {#if c.current && notify[c.name]}<span class="bell" title="Online / offline messages on">🔔</span>{/if}
                </span>
                {#if c.notes}<span class="mono faint small ip" title="Login (certificate name)">{c.name}</span>{/if}
                {#if !c.current}<span class="faint small">replaced</span>{/if}
                {#if c.current && staticIPs[c.name]}<span class="mono faint small ip">{staticIPs[c.name]}</span>{/if}
                {#if c.current && emails[c.name]}<span class="faint small ip">{emails[c.name]}</span>{/if}
              </td>
              <td class="c-status"><span class="pill {c.st.tone}">{c.st.label}</span></td>
              <td class="c-valid">
                {#if c.status === 'revoked'}
                  <span class="muted">Revoked {date(c.revoked_at)}</span>
                {:else}
                  {date(c.not_after)} <span class="faint small">({relative(c.not_after)})</span>
                {/if}
              </td>
              <td class="hide-s"><span class="mono faint" title={c.serial}>{c.serial.slice(0, 8)}…</span></td>
              <td class="actions">
                {#if c.current}
                  {#if c.st.key === 'valid' || c.st.key === 'expiring'}
                    <a class="btn small" href={apiURL(`/certs/${encodeURIComponent(c.name)}/ovpn`)} download="{c.name}.ovpn">Download profile</a>
                  {/if}
                  {#if c.status !== 'revoked'}
                    <button class="btn small ghost warn-btn" onclick={() => ask('revoke', c.name)}>Revoke</button>
                  {:else}
                    <button class="btn small ghost" onclick={() => act('unrevoke', c.name)}>Restore</button>
                  {/if}
                  <details class="menu" ontoggle={placeMenu}>
                    <summary class="btn small ghost" aria-label="More actions for {c.name}">More</summary>
                    <div class="menu-list">
                      {#if online.has(c.name)}
                        <button onclick={() => act('disconnect', c.name)}>Disconnect</button>
                      {/if}
                      {#if c.st.key === 'valid' || c.st.key === 'expiring'}
                        <button onclick={(e) => { e.currentTarget.closest('details').open = false; mailing = { open: true, name: c.name }; }}>Email the profile</button>
                      {/if}
                      {#if c.status !== 'revoked'}
                        <button onclick={(e) => { e.currentTarget.closest('details').open = false; openNaming(c.name); }}>Display name</button>
                        <button onclick={(e) => { e.currentTarget.closest('details').open = false; setNotify(c.name, !notify[c.name]); }}>{notify[c.name] ? 'Online / offline messages: off' : 'Online / offline messages: on'}</button>
                        <button onclick={(e) => { e.currentTarget.closest('details').open = false; network = { open: true, name: c.name }; }}>Network</button>
                        <button onclick={(e) => { e.currentTarget.closest('details').open = false; twoFactor = { open: true, name: c.name }; }}>Two-factor login</button>
                        <button onclick={() => ask('renew', c.name)}>Renew</button>
                      {/if}
                      <button class="danger" onclick={() => ask('delete', c.name)}>Delete</button>
                    </div>
                  </details>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

<label class="history">
  <input type="checkbox" bind:checked={showHistory} />
  Show replaced certificates
</label>

<Dialog bind:open={createOpen} title="New client">
  <form id="create" class="form" onsubmit={create}>
    {#if formError}<div class="banner bad" role="alert">{formError}</div>{/if}
    <label class="field">
      <span>Name</span>
      <input bind:value={form.name} placeholder="e.g. alice-phone" autocomplete="off" autocapitalize="none" spellcheck="false" required />
      <span class="hint" class:bad={form.name && !nameOk}>The login and certificate name, can't be changed later. Letters, digits, dot, dash, underscore or @.</span>
    </label>
    <label class="field">
      <span>Display name (optional)</span>
      <input bind:value={form.notes} placeholder="e.g. Alice's phone" maxlength="200" autocomplete="off" />
      <span class="hint">Shown in the list instead of the login. You can change it any time.</span>
    </label>
    {#key createOpen}<Lifetime bind:days={form.validity} bind:preset={presetLife} {defaultDays} />{/key}
    {#if presetLife && Number(form.validity) !== defaultDays}
      <label class="check small"><input type="checkbox" bind:checked={form.makeDefault} /> Use this for new clients and renewals on this server</label>
    {/if}
    <label class="field">
      <span>Key passphrase (optional)</span>
      <div class="pass">
        <input type={showPass ? 'text' : 'password'} bind:value={form.passphrase} minlength="4" maxlength="1024" autocomplete="new-password" />
        <button type="button" class="btn ghost small" onclick={() => (showPass = !showPass)}>{showPass ? 'Hide' : 'Show'}</button>
      </div>
      <span class="hint">The device asks for it on every connect. Leave empty for none.</span>
    </label>
  </form>
  {#snippet actions()}
    <button class="btn" onclick={() => (createOpen = false)}>Cancel</button>
    <button class="btn primary" form="create" type="submit" disabled={busy || !nameOk}>{busy ? 'Creating…' : 'Create client'}</button>
  {/snippet}
</Dialog>

<Dialog bind:open={bulk.open} title="Renew {selected.size} {selected.size === 1 ? 'certificate' : 'certificates'}">
  {#if bulk.results}
    <ul class="results">
      {#each bulk.results as r (r.name)}
        <li><span class="pill {r.ok ? 'ok' : 'bad'}">{r.name}</span> <span class="small muted">{r.ok ? 'renewed' : r.error}</span></li>
      {/each}
    </ul>
    <p class="hint">Renewed devices stop working with their old profile: download the new one for each.</p>
  {:else}
    <p>New certificates are issued with the same keys. The old ones stop working, so each device needs its new profile.</p>
    <p class="small muted names">{[...selected].join(', ')}</p>
    {#key bulk.open}<Lifetime bind:days={bulk.validity} {defaultDays} />{/key}
  {/if}
  {#snippet actions()}
    {#if bulk.results}
      <button class="btn" onclick={() => (bulk.open = false)}>Done</button>
    {:else}
      <button class="btn" onclick={() => (bulk.open = false)}>Cancel</button>
      <button class="btn primary" disabled={bulk.busy} onclick={renewSelected}>{bulk.busy ? 'Renewing…' : `Renew ${selected.size}`}</button>
    {/if}
  {/snippet}
</Dialog>

<TwoFactor bind:open={twoFactor.open} name={twoFactor.name} />
<NamedNetworks bind:open={namedOpen} clients={namedClients} onsaved={load} />
<NetworkDialog bind:open={network.open} name={network.name} online={online.has(network.name)} onsaved={load} />

<Dialog bind:open={confirm.open} title={confirmText[confirm.kind]?.title ?? ''}>
  <p><strong>{confirm.name}</strong>: {confirmText[confirm.kind]?.body}</p>
  {#snippet actions()}
    <button class="btn" onclick={() => (confirm.open = false)}>Cancel</button>
    <button class="btn {confirm.kind === 'renew' ? 'primary' : 'danger solid'}" onclick={() => act(confirm.kind, confirm.name)}>
      {confirmText[confirm.kind]?.button}
    </button>
  {/snippet}
</Dialog>

<Dialog bind:open={naming.open} title="Display name">
  <form id="naming" class="form" onsubmit={saveNaming}>
    {#if naming.error}<div class="banner bad" role="alert">{naming.error}</div>{/if}
    <label class="field">
      <span>Display name of <span class="mono">{naming.name}</span></span>
      <input bind:value={naming.notes} placeholder="e.g. Alice's phone" maxlength="200" autocomplete="off" />
      <span class="hint">Shown in the list instead of the login. The login stays {naming.name}; profiles keep working. Leave empty to show the login.</span>
    </label>
  </form>
  {#snippet actions()}
    <button class="btn" onclick={() => (naming.open = false)}>Cancel</button>
    <button class="btn primary" form="naming" type="submit" disabled={naming.busy}>{naming.busy ? 'Saving…' : 'Save'}</button>
  {/snippet}
</Dialog>

<SendProfile bind:open={mailing.open} name={mailing.name} email={emails[mailing.name] ?? ''} onsent={(to) => (emails = { ...emails, [mailing.name]: to })} />

<style>
  .bell { margin-left: 4px; font-size: 12px; cursor: help; }
  .head-actions { display: flex; gap: var(--space-2); flex-wrap: wrap; }
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-4); margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }

  .toolbar { display: flex; flex-wrap: wrap; gap: var(--space-3); align-items: center; justify-content: space-between; margin-bottom: var(--space-3); }
  .tabs { display: flex; flex-wrap: wrap; gap: 2px; }
  .tabs button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 32px;
    padding: 0 var(--space-3);
    border: 0;
    border-radius: var(--radius-s);
    background: transparent;
    color: var(--ink-2);
    font: inherit;
    font-weight: 500;
    cursor: pointer;
  }
  .tabs button:hover { color: var(--ink); background: var(--sunken); }
  .tabs button[aria-selected='true'] { color: var(--ink); background: var(--surface); box-shadow: inset 0 0 0 1px var(--line); }
  .count { font-size: 12px; color: var(--ink-3); }
  .search { width: 220px; }
  .banner { margin-bottom: var(--space-3); }

  .name { display: inline-flex; align-items: center; gap: 6px; font-weight: 500; }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--ok); }
  .ip { margin-left: var(--space-2); }
  th.sel, td.sel { width: 36px; padding-right: 0; }
  td.sel input, th.sel input { height: auto; }
  tr.picked td { background: color-mix(in srgb, var(--accent-soft) 60%, transparent); }
  .attention { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); margin-bottom: var(--space-3); }
  .bulkbar {
    position: sticky;
    top: 0;
    z-index: 5;
    display: flex;
    align-items: center;
    gap: var(--space-3);
    margin-bottom: var(--space-3);
    padding: var(--space-2) var(--space-3);
    border-radius: var(--radius-s);
    background: var(--accent-soft);
  }
  .results { list-style: none; margin: 0; padding: 0; display: grid; gap: 6px; max-height: 40vh; overflow: auto; }
  .names { max-height: 6em; overflow: auto; }
  tr.old td { color: var(--ink-3); }
  tr.old .name { font-weight: 400; }
  td.actions { text-align: right; white-space: nowrap; }
  td.actions > * { vertical-align: middle; }


  .history { display: inline-flex; align-items: center; gap: var(--space-2); margin-top: var(--space-3); color: var(--ink-2); font-size: var(--step--1); }
  .history input { height: auto; }

  .form { display: grid; gap: var(--space-4); }
  .pass { display: flex; gap: var(--space-2); }
  .pass input { flex: 1; }
  .hint.bad { color: var(--bad); }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }

  /* Phones: each row becomes a small card: name + status, date, actions */
  @media (max-width: 760px) {
    .search { width: 100%; }
    .hide-s { display: none; }
    thead { display: none; }
    tbody tr {
      display: grid;
      grid-template-columns: 1fr auto;
      grid-template-areas: 'name status' 'valid valid' 'act act';
      gap: 6px var(--space-3);
      padding: var(--space-3) var(--space-4);
      border-bottom: 1px solid var(--line);
    }
    tbody tr:last-child { border-bottom: 0; }
    tbody td { display: block; padding: 0; border: 0; }
    tbody tr:hover td { background: none; }
    tbody tr { grid-template-columns: auto 1fr auto; grid-template-areas: 'sel name status' 'sel valid valid' 'sel act act'; }
    td.sel { grid-area: sel; width: auto; }
    .c-name { grid-area: name; }
    .c-status { grid-area: status; }
    .c-valid { grid-area: valid; font-size: var(--step--1); }
    td.actions { grid-area: act; text-align: left; }
  }
</style>
